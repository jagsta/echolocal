package config

import (
	"sync"

	"github.com/ygelfand/libcountertop/pkg/settings/schema"
	"github.com/ygelfand/libcountertop/pkg/settings/storage"

	"github.com/ygelfand/echolocal/internal/layout"
)

var once sync.Once
var shared *Store
var loadErr error

func store() *Store    { once.Do(func() { shared, loadErr = Load(layout.StatePath) }); return shared }
func Get() Config      { return store().Get() }
func Set() Writer      { return store().Set() }
func LoadError() error { store(); return loadErr }
func Started(d Device) { store().started(d) }
func Use(path string)  { once.Do(func() {}); shared, loadErr = Load(path) }

type Store struct {
	*storage.Store[Config]
	shared schema.SharedWriter[Config]
}

// migrateIntercomEffects folds the pre-Alexa-colour default effect names
// (LED card t_05fc05ad) onto the stock Alexa entries. Configs written BEFORE
// the recolour carry the then-defaults "Beacon"/"Pulse" verbatim; without
// this every deployed dot keeps the old red ring/in-call shows. The values
// are moved, not dropped: an explicitly chosen Beacon/Pulse is migrated too,
// the accepted cost — dots show Alexa colours unless reconfigured afterwards.
func migrateIntercomEffects(c *Config) error {
	if c.Intercom.Ring == "Beacon" {
		c.Intercom.Ring = "Drop-in"
	}
	if c.Intercom.Call == "Pulse" {
		c.Intercom.Call = "Drop-in Pulse"
	}
	return nil
}

func Load(path string) (*Store, error) {
	s, err := storage.Load(
		path,
		Defaults(),
		cloneConfig,
		func(data []byte, c *Config) error {
			if err := schema.MigrateNetwork(data, &c.Network); err != nil {
				return err
			}
			return migrateIntercomEffects(c)
		},
		func(data []byte, c *Config) error { return schema.MigratePhotos(data, &c.Photos) },
	)
	shared := schema.NewSections(func() *storage.Store[Config] { return s }, sharedOf).Writer()
	return &Store{Store: s, shared: shared}, err
}
func (s *Store) Set() Writer      { return Writer{SharedWriter: s.shared, st: s} }
func (s *Store) started(d Device) { s.Runtime(func(c *Config) { c.Device = d }) }
func cloneConfig(c Config) Config {
	c.Shared = c.Clone()
	return c
}
