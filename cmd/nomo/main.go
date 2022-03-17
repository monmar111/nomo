package main

import (
	"context"
	"crypto/tls"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/KDF5000/pkg/larkbot"
	"github.com/KDF5000/pkg/log"
	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
	"github.com/joho/godotenv"

	"github.com/KDF5000/nomo/application"
	"github.com/KDF5000/nomo/infrastructure/persistence"
	"github.com/KDF5000/nomo/interfaces"
)

func initLog() {
	var logFile string
	if logFile = os.Getenv("NOMO_LOG_FILE"); logFile == "" {
		logFile = "/var/log/nomo.log"
	}

	var tops = []log.TeeOption{
		{
			Filename: logFile,
			RotateOpt: log.RotateOptions{
				MaxSize:    64 * 1024 * 1024,
				MaxAge:     7,
				MaxBackups: 10,
				Compress:   true,
			},
			Level: log.InfoLevel,
		},
	}

	logger := log.NewTeeWithRotate(tops)
	log.ResetDefault(logger)
}

func main() {
	// env file must be in the same path with binary file
	dir, _ := filepath.Abs(filepath.Dir(os.Args[0]))
	godotenv.Load(fmt.Sprintf("%s/.env", dir))

	initLog()
	log.Infof(".env file may has loaded. path=%s/.env", dir)
	host := os.Getenv("DB_HOST")
	password := os.Getenv("DB_PASSWORD")
	user := os.Getenv("DB_USER")
	dbname := os.Getenv("DB_NAME")
	port := os.Getenv("DB_PORT")
	// http env
	addr := fmt.Sprintf("%s:%s", os.Getenv("HTTP_ADDR"), os.Getenv("HTTP_PORT"))

	repos, err := persistence.NewRepositories(user, password, port, host, dbname)
	if err != nil {
		panic(err)
	}
	if err := repos.AutoMigrate(); err != nil {
		log.Fatal(err.Error())
	}

	// register routers
	router := gin.Default()
	router.Use(cors.New(cors.Config{
		AllowOrigins: []string{"*"},
		AllowMethods: []string{"GET", "POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS"},
		AllowHeaders: []string{
			"Origin", "Content-Length", "Content-Type", "Authorization",
			"Keep-Alive", "User-Agent", "X-Mx-ReqToken", "X-Requested-With", "Cache-Control",
			"If-Modified-Since", "DNT",
		},
		AllowCredentials: true,
		MaxAge:           12 * time.Hour,
	}))

	router.GET("/ping", func(c *gin.Context) {
		c.JSON(http.StatusOK, "ping succ")
	})

	bot := larkbot.NewLarkBot(larkbot.BotOption{
		AppID:     os.Getenv("LARK_APP_ID"),
		AppSecret: os.Getenv("LARK_APP_SECRET"),
	})

	adminUserID := os.Getenv("ADMIN_USERID")
	notify := func(msg string) {
		if adminUserID != "" {
			bot.SendTextMessage(larkbot.IDTypeUserID, adminUserID, "", msg)
		} else {
			log.Infof("Notify ==> %s", msg)
		}
	}

	bindHander := interfaces.NewBindHandler(
		application.NewBindInfoApp(repos.BindInfoRepo))
	larkMsgHandler := interfaces.NewLarkMessageHandler(
		application.NewLarkMessageHandleApp(repos.BindInfoRepo, repos.LarkBotRegistarRepo, notify))

	v1 := router.Group("/api/v1")
	v1.POST("/bind/wx", bindHander.BindWX)
	v1.POST("/bind/lark", bindHander.BindLark)
	v1.POST("/message/lark", larkMsgHandler.HandleMessage)
	// v1.POST("/message/lark", larkMsgHandler.UrlVerification)

	srv := &http.Server{
		Addr:    addr,
		Handler: router,
	}

	useHttps := false
	if os.Getenv("USE_HTTPS") != "" {
		b, err := strconv.ParseBool(os.Getenv("USE_HTTPS"))
		if err != nil {
			log.Fatalf("invalid USE_HTTPS env. %v", err)
		}

		useHttps = b
	}

	if useHttps {
		srv.TLSConfig = &tls.Config{
			// MinVersion:               tls.VersionTLS13,
			PreferServerCipherSuites: true,
		}
	}

	// Initializing the server in a goroutine so that
	// it won't block the graceful shutdown handling below
	go func() {
		log.Infof("begin to start http/https server on %s(https: %v)...", addr, useHttps)
		var err error
		if useHttps {
			err = srv.ListenAndServeTLS(os.Getenv("HTTPS_CERT_FILE"), os.Getenv("HTTPS_KEY_FILE"))
		} else {
			err = srv.ListenAndServe()
		}
		if err != nil && err != http.ErrServerClosed {
			log.Fatalf("listen: %s\n", err)
		}
	}()

	// Wait for interrupt signal to gracefully shutdown the server with
	// a timeout of 5 seconds.
	quit := make(chan os.Signal, 1)
	// kill (no param) default send syscall.SIGTERM
	// kill -2 is syscall.SIGINT
	// kill -9 is syscall.SIGKILL but can't be catch, so don't need add it
	signal.Notify(quit, syscall.SIGHUP, syscall.SIGINT, syscall.SIGTERM)
	sig := <-quit
	log.Infof("Receive signal `%v`, shutting down server...\n", sig)
	// The context is used to inform the server it has 5 seconds to finish
	// the request it is currently handling
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		log.Fatalf("Server forced to shutdown: %v", err)
	}

	log.Info("Server exiting")
}
