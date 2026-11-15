package server

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"booksmith/internal/httpx"
	"booksmith/internal/jsenv"
	"booksmith/internal/store"
	"booksmith/internal/webbook"
)

func TestDebugSfacgBody(t *testing.T) {
	dir := `C:/Users/admin/AppData/Local/Temp/bsdata`
	st, err := store.Open(dir + "/booksmith.db")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	client := httpx.NewClient()
	wb := webbook.New(client, jsenv.New(client, dir))
	app := &App{Store: st, DataDir: dir, WebBook: wb}
	bsAny, _ := app.Store.GetSource("http://book.sfacg.com")
	bs := bsAny
	// 直接走 fetchWithRule 同款路径
	body, final, err := wb.DebugFetch(context.Background(), bs, "https://book.sfacg.com/Novel/217202/MainIndex/", "http://book.sfacg.com")
	if err != nil {
		t.Fatal(err)
	}
	fmt.Printf("len=%d final=%s catalog-list 命中=%d story-catalog=%d\n",
		len(body), final,
		strings.Count(body, "catalog-list"),
		strings.Count(body, "story-catalog"))
	fmt.Printf("head=%q\n", body[:min(200, len(body))])
}

func min(a, b int) int { if a < b { return a }; return b }
