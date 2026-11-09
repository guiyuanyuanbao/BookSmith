package webbook

import (
	"context"
	"testing"
	"time"

	"booksmith/internal/model"
	"booksmith/internal/testutil"
)

// 并发搜索墙钟：4 个 1.2s 慢源并行应显著快于串行（4.8s）。
func TestSearchParallelWallClock(t *testing.T) {
	svc, fs := newTestService(t)
	sources := make([]*model.BookSource, 0, 4)
	for i := 0; i < 4; i++ {
		bs := testutil.FixtureSlowSource(fs.Base)
		bs.BookSourceURL = fs.Base + "/slow" + string(rune('a'+i))
		sources = append(sources, bs)
	}
	start := time.Now()
	agg := svc.Search(context.Background(), sources, "慢", nil)
	wall := time.Since(start)
	if agg.Searched != 4 {
		t.Fatalf("searched = %d", agg.Searched)
	}
	if len(agg.Items) == 0 {
		t.Fatalf("no items")
	}
	if wall >= 3500*time.Millisecond {
		t.Fatalf("parallel search wall clock = %v, should be well under serial 4.8s", wall)
	}
	t.Logf("4 slow sources wall=%v items=%d", wall, len(agg.Items))
}

// 空 searchUrl 源不计入总数。
func TestSearchSkipsEmptySearchURL(t *testing.T) {
	svc, fs := newTestService(t)
	bs := testutil.FixtureSlowSource(fs.Base)
	empty := &model.BookSource{BookSourceName: "无搜索", BookSourceURL: fs.Base + "/none"}
	agg := svc.Search(context.Background(), []*model.BookSource{bs, empty}, "慢", nil)
	if agg.Total != 1 {
		t.Errorf("total = %d want 1 (跳过空 searchUrl)", agg.Total)
	}
}

// 同一来源的多次命中只保留一个 origin（legado origins 集合语义）。
func TestMergeOriginsDedup(t *testing.T) {
	in := []*model.SearchOrigin{
		{SourceURL: "https://a.com", SourceName: "甲", Name: "书", BookURL: "/1"},
		{SourceURL: "https://a.com", SourceName: "甲", Name: "书", BookURL: "/1"},
		{SourceURL: "https://b.com", SourceName: "乙", Name: "书", BookURL: "/2"},
	}
	out := mergeSearchItems(nil, in)
	if len(out) != 1 {
		t.Fatalf("books = %d", len(out))
	}
	if len(out[0].Origins) != 2 {
		t.Fatalf("origins = %d want 2 (同源去重)", len(out[0].Origins))
	}
}

// 非搜索源（searchUrl 空）不应产生错误条目。
func TestSearchNoNoiseErrors(t *testing.T) {
	svc, fs := newTestService(t)
	bs := testutil.FixtureSlowSource(fs.Base)
	agg := svc.Search(context.Background(), []*model.BookSource{bs}, "慢", nil)
	if len(agg.Errors) != 0 {
		t.Errorf("errors = %v", agg.Errors)
	}
}
