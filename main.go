package main

import (
	"bufio"
	"fmt"
	"freechatgpt/internal/tokens"
	"freechatgpt/pkg/db"
	"freechatgpt/pkg/env"
	"freechatgpt/pkg/funcaptcha"
	"freechatgpt/pkg/logger"
	"freechatgpt/pkg/plugins"
	"freechatgpt/pkg/plugins/api/unofficialapi"
	"log"
	"net/http"
	"os"
	"strings"

	// "github.com/acheong08/endless"
	"github.com/gin-gonic/gin"
	"github.com/joho/godotenv"
)

var HOST string
var PORT string
var ACCESS_TOKENS tokens.AccessToken
var proxies []string
var tlsCert, tlsKey string

func checkProxy() {
	// first check for proxies.txt
	proxies = []string{}
	if _, err := os.Stat("proxies.txt"); err == nil {
		// Each line is a proxy, put in proxies array
		file, _ := os.Open("proxies.txt")
		defer file.Close()
		scanner := bufio.NewScanner(file)
		for scanner.Scan() {
			// Split line by :
			proxy := scanner.Text()
			proxy_parts := strings.Split(proxy, ":")
			if len(proxy_parts) > 1 {
				proxies = append(proxies, proxy)
			} else {
				continue
			}
		}
	}
	// if no proxies, then check env http_proxy
	if len(proxies) == 0 {
		proxy := os.Getenv("http_proxy")
		if proxy != "" {
			proxies = append(proxies, proxy)
		}
	}
}

func init() {
	_ = godotenv.Load(".env")
	tlsCert = os.Getenv("TLS_CERT")
	tlsKey = os.Getenv("TLS_KEY")

	HOST = os.Getenv("SERVER_HOST")
	PORT = os.Getenv("SERVER_PORT")
	if HOST == "" {
		HOST = "127.0.0.1"
	}
	if PORT == "" {
		PORT = "8080"
	}
	checkProxy()
	readAccounts()
	scheduleTokenPUID()
}
func main() {
	router := gin.Default()

	router.Use(cors)

	router.GET("/ping", func(c *gin.Context) {
		c.JSON(200, gin.H{
			"message": "pong",
		})
	})

	admin_routes := router.Group("/admin")
	admin_routes.Use(adminCheck)

	/// Admin routes
	admin_routes.PATCH("/password", passwordHandler)
	admin_routes.PATCH("/tokens", tokensHandler)
	/// Public routes
	router.Use(RouteHTTPOrWssMiddleware())
	component := &plugins.Component{
		Engine: router,
		Db: db.DB{
			GetRedisClient: db.GetRedisClient,
		},
		Logger: logger.Log,
		Env:    &env.Env,
		Auth:   funcaptcha.GetOpenAIArkoseToken,
	}
	targetPlug := &unofficialapi.UnofficialApiProcessInstance
	targetPlug.Run(component)
	// router.OPTIONS("/v1/chat/completions", optionsHandler)
	// router.POST("/v1/chat/completions", Authorization, nightmare)
	router.GET("/models", Authorization, simulateModel)
	if err := http.ListenAndServeTLS(fmt.Sprintf("%v:%v", HOST, PORT), tlsCert, tlsKey, router); err != nil {
		log.Fatal(err)
	}
}
