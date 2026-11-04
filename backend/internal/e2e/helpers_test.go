package e2e_test

import (
	"beeloy.com/backend/internal/contentsync"
	"beeloy.com/backend/internal/server/servertest"
)

func newSyncer(env *servertest.Env) *contentsync.ArticleSyncer {
	s := env.Server
	return &contentsync.ArticleSyncer{Src: env.Store, Pool: s.Pool, Store: &contentsync.Store{Pool: s.Pool}, Slugs: s.Slugs}
}
