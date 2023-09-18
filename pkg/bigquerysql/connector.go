package bigquerysql

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"strings"

	"cloud.google.com/go/bigquery"
	"github.com/pingcap-inc/tidb2dw/pkg/tidbsql"
	"github.com/pingcap/errors"
	"github.com/pingcap/log"
	"github.com/pingcap/tiflow/pkg/sink/cloudstorage"
	"go.uber.org/zap"
)

type BigQueryConnector struct {
	bqClient *bigquery.Client

	datasetID        string
	tableID          string
	incrementTableID string
	storageURL       string

	columns []cloudstorage.TableCol
}

func NewBigQueryConnector(bqClient *bigquery.Client, incrementTableID, datasetID, tableID string, storageURI *url.URL) (*BigQueryConnector, error) {
	storageURL := fmt.Sprintf("%s://%s%s", storageURI.Scheme, storageURI.Host, storageURI.Path)
	return &BigQueryConnector{
		bqClient:         bqClient,
		datasetID:        datasetID,
		tableID:          tableID,
		incrementTableID: incrementTableID,
		storageURL:       storageURL,
		columns:          nil,
	}, nil
}

func (bc *BigQueryConnector) InitSchema(columns []cloudstorage.TableCol) error {
	if len(bc.columns) != 0 {
		return nil
	}
	if len(columns) == 0 {
		return errors.New("Columns in schema is empty")
	}
	bc.columns = columns
	log.Info("table columns initialized", zap.Any("Columns", columns))
	return nil
}

func (bc *BigQueryConnector) ExecDDL(tableDef cloudstorage.TableDefinition) error {
	if len(bc.columns) == 0 {
		return errors.New("Columns not initialized. Maybe you execute a DDL before all DMLs, which is not supported now.")
	}
	ddls, err := GenDDLViaColumnsDiff(bc.datasetID, bc.tableID, bc.columns, tableDef)
	if err != nil {
		return errors.Trace(err)
	}
	if len(ddls) == 0 {
		log.Info("No need to execute this DDL in Snowflake", zap.String("ddl", tableDef.Query))
		return nil
	}
	// One DDL may be rewritten to multiple DDLs
	ctx := context.Background()
	for _, ddl := range ddls {
		job, err := bc.bqClient.Query(ddl).Run(ctx)
		if err != nil {
			log.Error("Failed to execute DDL", zap.String("received", tableDef.Query), zap.String("rewritten", strings.Join(ddls, "\n")))
			return errors.Annotate(err, fmt.Sprint("failed to execute", ddl))
		}
		status, err := job.Wait(ctx)
		if err != nil {
			log.Error("Failed to wait DDL", zap.String("received", tableDef.Query), zap.String("rewritten", strings.Join(ddls, "\n")))
			return errors.Annotate(err, fmt.Sprint("failed to wait", ddl))
		}
		if status.Err() != nil {
			log.Error("Failed to executed DDL", zap.String("received", tableDef.Query), zap.String("rewritten", strings.Join(ddls, "\n")))
			return errors.Annotate(err, fmt.Sprint("failed to execute", ddl))
		}
	}
	// update columns
	bc.columns = tableDef.Columns
	log.Info("Successfully executed DDL", zap.String("received", tableDef.Query), zap.String("rewritten", strings.Join(ddls, "\n")))
	return nil
}

// CopyTableSchema copies table schema from TiDB to BigQuery
// If table exists, delete it first
func (bc *BigQueryConnector) CopyTableSchema(sourceDatabase string, sourceTable string, sourceTiDBConn *sql.DB) error {
	ctx := context.Background()

	tableColumns, err := tidbsql.GetTiDBTableColumn(sourceTiDBConn, sourceDatabase, sourceTable)
	if err != nil {
		return errors.Trace(err)
	}

	pKColumns, err := tidbsql.GetTiDBTablePKColumns(sourceTiDBConn, sourceDatabase, sourceTable)
	if err != nil {
		return errors.Trace(err)
	}

	createTableSQL, err := GenCreateSchema(tableColumns, pKColumns, bc.datasetID, bc.tableID)
	if err != nil {
		return errors.Trace(err)
	}
	if err = runQuery(ctx, bc.bqClient, createTableSQL); err != nil {
		return errors.Annotate(err, "Failed to create table")
	}
	return nil
}

func (bc *BigQueryConnector) LoadSnapshot(targetTable, filePrefix string, onSnapshotLoadProgress func(loadedRows int64)) error {
	ctx := context.Background()
	gcsFilePath := fmt.Sprintf("%s/%s*.csv", bc.storageURL, filePrefix)
	// FIXME: if source table is empty, bigquery will fail to load (file not found)
	err := loadGCSFileToBigQuery(ctx, bc.bqClient, bc.datasetID, bc.tableID, gcsFilePath)
	if err != nil {
		return errors.Trace(err)
	}
	return nil
}

func (bc *BigQueryConnector) LoadIncrement(tableDef cloudstorage.TableDefinition, uri *url.URL, filePath string) error {
	ctx := context.Background()
	incrementTableID := bc.incrementTableID
	absolutePath := fmt.Sprintf("%s://%s%s/%s", uri.Scheme, uri.Host, uri.Path, filePath)

	tableColumns := GenIncrementTableColumns(tableDef.Columns)
	createTableSQL, err := GenCreateSchema(tableColumns, []string{}, bc.datasetID, incrementTableID)
	if err != nil {
		return errors.Trace(err)
	}
	if err = runQuery(ctx, bc.bqClient, createTableSQL); err != nil {
		return errors.Annotate(err, "Failed to create increment table")
	}

	err = loadGCSFileToBigQuery(ctx, bc.bqClient, bc.datasetID, incrementTableID, absolutePath)
	if err != nil {
		return errors.Trace(err)
	}

	mergeSQL := GenMergeInto(tableDef, bc.datasetID, bc.tableID, incrementTableID)
	if err = runQuery(ctx, bc.bqClient, mergeSQL); err != nil {
		return errors.Annotate(err, "Failed to merge increment table")
	}

	err = deleteTable(ctx, bc.bqClient, bc.datasetID, incrementTableID)
	if err != nil {
		return errors.Trace(err)
	}

	log.Info("Successfully merge file", zap.String("file", filePath))
	return nil
}

func (bc *BigQueryConnector) Close() {
	bc.bqClient.Close()
}
