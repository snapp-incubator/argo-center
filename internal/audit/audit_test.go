package audit

import (
	"context"
	"path/filepath"
	"slices"
	"testing"

	"github.com/snapp-incubator/argo-center/internal/store"
)

func TestRecordAndList(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.db")
	open := func() (*Store, func()) {
		db, err := store.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		s, err := New(db)
		if err != nil {
			t.Fatal(err)
		}
		return s, func() { db.Close() }
	}
	s, closeDB := open()
	ctx := context.Background()
	for _, e := range []Entry{
		{User: "ali", Action: ActionDryRun, App: "api", Cluster: "cluster-a", Revision: "abc", Result: ResultOK},
		{User: "ali", Action: ActionSync, App: "api", Cluster: "cluster-a", Revision: "abc", Options: []string{"prune"}, Result: ResultOK},
		{User: "sara", Action: ActionSync, App: "web", Cluster: "cluster-b", Result: ResultError, Message: "boom"},
	} {
		if err := s.Record(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	closeDB()

	// Reopen to check the data survives.
	s, closeDB = open()
	defer closeDB()

	got, err := s.List(ctx, Filter{App: "api"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Action != ActionSync || got[1].Action != ActionDryRun {
		t.Fatalf("app filter / order: %+v", got)
	}
	if !slices.Equal(got[0].Options, []string{"prune"}) || got[0].Time.IsZero() || got[1].Options == nil {
		t.Errorf("fields not round-tripped: %+v", got)
	}

	got, _ = s.List(ctx, Filter{User: "sara", Cluster: "cluster-b", Limit: 1})
	if len(got) != 1 || got[0].Message != "boom" {
		t.Errorf("user+cluster filter: %+v", got)
	}

	got, _ = s.List(ctx, Filter{Action: ActionSync})
	if len(got) != 2 {
		t.Errorf("action filter: %+v", got)
	}
	page1, _ := s.List(ctx, Filter{Limit: 2})
	page2, _ := s.List(ctx, Filter{Limit: 2, Before: page1[1].ID})
	if len(page1) != 2 || len(page2) != 1 || page2[0].ID >= page1[1].ID {
		t.Errorf("paging: %+v then %+v", page1, page2)
	}
	if users, _ := s.Users(ctx); !slices.Equal(users, []string{"ali", "sara"}) {
		t.Errorf("users = %v", users)
	}
}
