package api

import (
	"encoding/json"
	"errors"
	"io"
	"log"
	"math/rand"
	"net/http"
	"os"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/generative-ai-go/genai"
	openai "github.com/sashabaranov/go-openai"
	"github.com/zhu327/gemini-openai-proxy/pkg/adapter"
	"google.golang.org/api/option"
)

func IndexHandler(c *gin.Context) {
	c.JSON(http.StatusMisdirectedRequest, gin.H{
		"message": "Welcome to the OpenAI API! Documentation is available at https://platform.openai.com/docs/api-reference",
	})
}

func ModelListHandler(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"object": "list",
		"data": []any{
			openai.Model{
				CreatedAt: 1686935002,
				ID:        openai.GPT3Dot5Turbo,
				Object:    "model",
				OwnedBy:   "openai",
			},
			openai.Model{
				CreatedAt: 1686935002,
				ID:        openai.GPT4VisionPreview,
				Object:    "model",
				OwnedBy:   "openai",
			},
		},
	})
}

func ModelRetrieveHandler(c *gin.Context) {
	model := c.Param("model")
	c.JSON(http.StatusOK, openai.Model{
		CreatedAt: 1686935002,
		ID:        model,
		Object:    "model",
		OwnedBy:   "openai",
	})
}

func ChatProxyHandler(c *gin.Context) {
	openaiAPIKey, err := getRandomAPIKey()
	if err != nil {
		log.Printf("Error getting API key: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to retrieve API key from gemini-api-key.json file"})
		return
	}

	req := &adapter.ChatCompletionRequest{}
	// Bind the JSON data from the request to the struct
	if err := c.ShouldBindJSON(req); err != nil {
		c.JSON(http.StatusBadRequest, openai.APIError{
			Code:    http.StatusBadRequest,
			Message: err.Error(),
		})
		return
	}

	// Model change logic based on the incoming model
	switch req.Model {
	case "gemini-pro":
		req.Model = "gpt-3.5-turbo"
	case "gemini-pro-vision":
		req.Model = "gpt-4-vision-preview"
	}

	messages, err := req.ToGenaiMessages()
	if err != nil {
		c.JSON(http.StatusBadRequest, openai.APIError{
			Code:    http.StatusBadRequest,
			Message: err.Error(),
		})
		return
	}

	ctx := c.Request.Context()
	client, err := genai.NewClient(ctx, option.WithAPIKey(openaiAPIKey))
	if err != nil {
		log.Printf("new genai client error %v\n", err)
		c.JSON(http.StatusBadRequest, openai.APIError{
			Code:    http.StatusBadRequest,
			Message: err.Error(),
		})
		return
	}
	defer client.Close()

	model := req.ToGenaiModel()
	gemini := adapter.NewGeminiAdapter(client, model)

	if !req.Stream {
		resp, err := gemini.GenerateContent(ctx, req, messages)
		if err != nil {
			log.Printf("genai generate content error %v\n", err)
			c.JSON(http.StatusBadRequest, openai.APIError{
				Code:    http.StatusBadRequest,
				Message: err.Error(),
			})
			return
		}

		c.JSON(http.StatusOK, resp)
		return
	}

	dataChan, err := gemini.GenerateStreamContent(ctx, req, messages)
	if err != nil {
		log.Printf("genai generate content error %v\n", err)
		c.JSON(http.StatusBadRequest, openai.APIError{
			Code:    http.StatusBadRequest,
			Message: err.Error(),
		})
		return
	}

	setEventStreamHeaders(c)
	c.Stream(func(w io.Writer) bool {
		if data, ok := <-dataChan; ok {
			c.Render(-1, adapter.Event{Data: "data: " + data})
			return true
		}
		c.Render(-1, adapter.Event{Data: "data: [DONE]"})
		return false
	})
}

func setEventStreamHeaders(c *gin.Context) {
	c.Writer.Header().Set("Content-Type", "text/event-stream")
	c.Writer.Header().Set("Cache-Control", "no-cache")
	c.Writer.Header().Set("Connection", "keep-alive")
	c.Writer.Header().Set("Transfer-Encoding", "chunked")
	c.Writer.Header().Set("X-Accel-Buffering", "no")
}

func getRandomAPIKey() (string, error) {
	// Read the content of the JSON file
	file, err := os.ReadFile("gemini-api-key.json")
	if err != nil {
		return "", err
	}

	// Create a map to hold the data from the JSON file
	var data map[string][]string

	// Unmarshal the JSON data into the map
	if err := json.Unmarshal(file, &data); err != nil {
		return "", err
	}

	// Retrieve the API keys array from the map
	keys, exists := data["api_keys"]
	if !exists || len(keys) == 0 {
		return "", errors.New("no API keys found in the file")
	}

	// Create a new random source and generator
	src := rand.NewSource(time.Now().UnixNano())
	rng := rand.New(src)

	// Select a random key from the array
	randomIndex := rng.Intn(len(keys))
	return keys[randomIndex], nil
}
