package cmd

import (
	"time"

	"github.com/pingcap-inc/tidb2dw/pkg/tidbsql"
	"github.com/pingcap/errors"
	"github.com/pingcap/log"
	"github.com/pingcap/tiflow/pkg/logutil"
	"github.com/spf13/cobra"
	"github.com/thediveo/enumflag"
	"go.uber.org/zap"
)

func NewGCSCmd() *cobra.Command {
	var (
		tidbConfigFromCli   tidbsql.TiDBConfig
		credentialsFilePath string
		tables              []string
		snapshotConcurrency int
		storagePath         string
		cdcHost             string
		cdcPort             int
		cdcFlushInterval    time.Duration
		cdcFileSize         int
		logFile             string
		logLevel            string
		csvOutputDialect    string
		mode                RunMode
	)

	run := func() error {
		err := logutil.InitLogger(&logutil.Config{
			Level: logLevel,
			File:  logFile,
		})
		if err != nil {
			return errors.Trace(err)
		}

		storageURI, err := getGCSURIWithCredentials(storagePath, credentialsFilePath)
		if err != nil {
			return errors.Trace(err)
		}

		snapshotURI, incrementURI, err := genSnapshotAndIncrementURIs(storageURI)
		if err != nil {
			return errors.Trace(err)
		}

		_, err = Export(
			&tidbConfigFromCli, tables, storageURI, snapshotURI, incrementURI, snapshotConcurrency,
			cdcHost, cdcPort, cdcFlushInterval, cdcFileSize, csvOutputDialect, mode)

		return err
	}

	cmd := &cobra.Command{
		Use:   "gcs",
		Short: "Export snapshot and incremental data from TiDB to GCS",
		Run: func(_ *cobra.Command, _ []string) {
			if err := run(); err != nil {
				log.Error("Fatal error running gcs exporter", zap.Error(err))
			}
		},
	}

	cmd.PersistentFlags().BoolP("help", "", false, "help for this command")
	cmd.Flags().Var(enumflag.New(&mode, "mode", RunModeIds, enumflag.EnumCaseInsensitive), "mode", "replication mode: full, snapshot-only, incremental-only, cloud")
	cmd.Flags().StringVarP(&tidbConfigFromCli.Host, "tidb.host", "h", "127.0.0.1", "TiDB host")
	cmd.Flags().IntVarP(&tidbConfigFromCli.Port, "tidb.port", "P", 4000, "TiDB port")
	cmd.Flags().StringVarP(&tidbConfigFromCli.User, "tidb.user", "u", "root", "TiDB user")
	cmd.Flags().StringVarP(&tidbConfigFromCli.Pass, "tidb.pass", "p", "", "TiDB password")
	cmd.Flags().StringVar(&tidbConfigFromCli.SSLCA, "tidb.ssl-ca", "", "TiDB SSL CA")
	cmd.Flags().StringVarP(&credentialsFilePath, "credentials-file-path", "", "", "Google application credentials file path")
	cmd.Flags().StringArrayVarP(&tables, "table", "t", []string{}, "tables full qualified name, e.g. -t <db1>.<table1> -t <db2>.<table2>")
	cmd.Flags().IntVar(&snapshotConcurrency, "snapshot-concurrency", 8, "the number of concurrent snapshot workers")
	cmd.Flags().StringVarP(&storagePath, "storage", "s", "", "storage path: gs://<bucket>/<path>")
	cmd.Flags().StringVar(&csvOutputDialect, "csv-output-dialect", "", "csv output dialect: default, redshift, snowflake, bigquery")
	cmd.Flags().StringVar(&cdcHost, "cdc.host", "127.0.0.1", "TiCDC server host")
	cmd.Flags().IntVar(&cdcPort, "cdc.port", 8300, "TiCDC server port")
	cmd.Flags().DurationVar(&cdcFlushInterval, "cdc.flush-interval", 60*time.Second, "")
	cmd.Flags().IntVar(&cdcFileSize, "cdc.file-size", 64*1024*1024, "")
	cmd.Flags().StringVar(&logFile, "log.file", "", "log file path")
	cmd.Flags().StringVar(&logLevel, "log.level", "info", "log level")

	cmd.MarkFlagRequired("storage")

	return cmd
}
