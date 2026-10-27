package app

import (
	"github.com/metavert-io/metarush/internal/cache"
	"github.com/metavert-io/metarush/internal/config"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/tools"
)

// OpenOffline builds an App without a database connection, for reports that
// must still work when MongoDB is unreachable (`sync status` on a plane). DB and
// Queue are nil; Media is nil when the store cannot even be configured.
func OpenOffline(cfg *config.Config, via string) (*App, error) {
	c, err := cache.New(cfg.Cache.Dir, cfg.Cache.MaxGB)
	if err != nil {
		return nil, err
	}
	ms, _ := NewMediaStore(cfg)
	a := &App{Cfg: cfg, Media: ms, Cache: c, Tools: tools.New(cfg.Tools),
		Actor: models.Actor{UserID: cfg.User.ID, UserName: cfg.User.Name, InstallationID: cfg.Installation.ID, Via: via}}
	a.openLog() // no database: entries wait in the spool
	return a, nil
}
