// fixture 启动模拟书站（开发/端到端测试用）。
package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"

	"booksmith/internal/testutil"
)

func main() {
	addr := flag.String("addr", ":18501", "监听地址")
	flag.Parse()
	fs := testutil.NewFixtureSite()
	log.Printf("fixture 书站启动: %s (HTML 源 / ，POST 源 /post ，JSON 源 /api)", fs.Base)
	http.Handle("/", fs.Mux)
	if err := http.ListenAndServe(*addr, nil); err != nil {
		fmt.Fprintln(log.Writer(), err)
	}
}
