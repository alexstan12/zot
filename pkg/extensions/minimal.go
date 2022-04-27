package extensions

import (
	"context"
	goSync "sync"
	"time"

	"github.com/gorilla/mux"
	"zotregistry.io/zot/pkg/api/config"
	"zotregistry.io/zot/pkg/log"
	"zotregistry.io/zot/pkg/storage"
)

// nolint: deadcode,unused
var DownloadTrivyDB = func(dbDir string, log log.Logger, updateInterval time.Duration) error {
	return nil
}

// // EnableExtensions ...
// var EnableExtensions = func(config *config.Config, log log.Logger, rootDir string) {
// 	log.Warn().Msg("skipping enabling extensions because given zot binary doesn't support " +
// 		"any extensions, please build zot full binary for this feature")
// }

// EnableMetricsExtension ...
var EnableMetricsExtension = func(config *config.Config, log log.Logger, rootDir string) {
	log.Warn().Msg("skipping enabling metrics extension because given zot binary doesn't support " +
		"this extension, please build a binary that includes this feature")
}
// EnableSearchExtension ...
var EnableSearchExtension = func(config *config.Config, log log.Logger, rootDir string) {
	log.Warn().Msg("skipping enabling search extension because given zot binary doesn't support " +
		"this extension, please build a binary that includes this feature")
}

// EnableSyncExtension ...
var EnableSyncExtension = func(ctx context.Context, config *config.Config, wg *goSync.WaitGroup,
	storeController storage.StoreController, log log.Logger,
) {
	log.Warn().Msg("skipping enabling sync extension because given zot binary doesn't support any extensions," +
		"please build zot full binary for this feature")
}

// EnableScrubExtension ...
var EnableScrubExtension = func(config *config.Config, storeController storage.StoreController,
	log log.Logger,
) {
	log.Warn().Msg("skipping enabling scrub extension because given zot binary doesn't support any extensions," +
		"please build zot full binary for this feature")
}

// // SetupRoutes ...
// var SetupRoutes = func(conf *config.Config, router *mux.Router, storeController storage.StoreController, log log.Logger) {
// 	log.Warn().Msg("skipping setting up extensions routes because given zot binary doesn't support " +
// 		"any extensions, please build zot full binary for this feature")
// }
// SetupMetricsRoutes ...
var SetupMetricsRoutes = func(conf *config.Config, router *mux.Router, storeController storage.StoreController, log log.Logger) {
	log.Warn().Msg("skipping setting up metrics routes because given zot binary doesn't support " +
		"metrics extension, please build a binary that includes this feature")
}
// SetupSearchRoutes ...
var SetupSearchRoutes = func(conf *config.Config, router *mux.Router, storeController storage.StoreController, log log.Logger) {
	log.Warn().Msg("skipping setting up search routes because given zot binary doesn't support " +
		"search extension, please build a binary that includes this feature")
}

// SyncOneImage ...
var SyncOneImage = func(config *config.Config, storeController storage.StoreController,
	repoName, reference string, isArtifact bool, log log.Logger,
) error {
	log.Warn().Msg("skipping syncing on demand because given zot binary doesn't support any extensions," +
		"please build zot full binary for this feature")

	return nil
}
