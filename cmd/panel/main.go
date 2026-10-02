package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
	"vibexui/internal/server"
	"vibexui/internal/store"
)

func main() {
	listen := flag.String("listen", "127.0.0.1:8080", "监听地址")
	dbPath := flag.String("db", "data/panel.db", "SQLite 路径")
	public := flag.String("public-url", "http://localhost:8080", "公开访问地址")
	downloads := flag.String("downloads-dir", "dist", "Agent 发行目录（包含 linux-amd64/linux-arm64）")
	flag.Parse()
	if len(*public) > 0 && !server.IsLoopback(*public) && !strings.HasPrefix(*public, "https://") {
		log.Fatal("公网面板必须使用 HTTPS public-url")
	}
	db, err := store.Open(*dbPath)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()
	app, err := server.New(db, server.Options{Username: os.Getenv("VIBEXUI_USERNAME"), Password: os.Getenv("VIBEXUI_PASSWORD"), PublicURL: *public, DownloadsDir: *downloads})
	if err != nil {
		log.Fatal(err)
	}
	srv := &http.Server{Addr: *listen, Handler: app, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 20 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go app.RunMaintenance(ctx)
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		srv.Shutdown(shutdown)
	}()
	log.Printf("VibeXUI 面板：%s（监听 %s）", *public, *listen)
	if err = srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}
