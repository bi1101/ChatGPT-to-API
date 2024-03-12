package chatgpt

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"freechatgpt/typings"
	chatgpt_types "freechatgpt/typings/chatgpt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	hp "net/http"

	"github.com/gorilla/websocket"

	http "github.com/bogdanfinn/fhttp"
	tls_client "github.com/bogdanfinn/tls-client"
	"github.com/bogdanfinn/tls-client/profiles"
	"github.com/gin-gonic/gin"

	chatgpt_response_converter "freechatgpt/conversion/response/chatgpt"

	official_types "freechatgpt/typings/official"
)

type connInfo struct {
	conn   *websocket.Conn
	uuid   string
	expire time.Time
	ticker *time.Ticker
	lock   bool
}

var (
	client, _ = tls_client.NewHttpClient(tls_client.NewNoopLogger(), []tls_client.HttpClientOption{
		tls_client.WithCookieJar(tls_client.NewCookieJar()),
		tls_client.WithTimeoutSeconds(600),
		tls_client.WithClientProfile(profiles.Okhttp4Android13),
	}...)
	API_REVERSE_PROXY   = os.Getenv("API_REVERSE_PROXY")
	FILES_REVERSE_PROXY = os.Getenv("FILES_REVERSE_PROXY")
	connPool            = map[string][]*connInfo{}
)

func getWSURL(token string, retry int) (string, error) {
	request, err := http.NewRequest(http.MethodPost, "https://chat.openai.com/backend-api/register-websocket", nil)
	if err != nil {
		return "", err
	}
	request.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/117.0.0.0 Safari/537.36")
	request.Header.Set("Accept", "*/*")
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := client.Do(request)
	if err != nil {
		if retry > 3 {
			return "", err
		}
		time.Sleep(time.Second) // wait 1s to get ws url
		return getWSURL(token, retry+1)
	}
	defer response.Body.Close()
	var WSSResp chatgpt_types.ChatGPTWSSResponse
	err = json.NewDecoder(response.Body).Decode(&WSSResp)
	if err != nil {
		return "", err
	}
	return WSSResp.WssUrl, nil
}

func createWSConn(url string, connInfo *connInfo, retry int) error {
	header := make(hp.Header)
	header.Add("Sec-WebSocket-Protocol", "json.reliable.webpubsub.azure.v1")
	dialer := websocket.Dialer{
		Proxy:             hp.ProxyFromEnvironment,
		HandshakeTimeout:  45 * time.Second,
		EnableCompression: true,
	}
	conn, _, err := dialer.Dial(url, header)
	if err != nil {
		if retry > 3 {
			return err
		}
		time.Sleep(time.Second) // wait 1s to recreate ws
		return createWSConn(url, connInfo, retry+1)
	}
	connInfo.conn = conn
	connInfo.expire = time.Now().Add(time.Minute * 30)
	ticker := time.NewTicker(time.Second * 8)
	connInfo.ticker = ticker
	go func(ticker *time.Ticker) {
		defer ticker.Stop()
		for {
			<-ticker.C
			if err := connInfo.conn.WriteMessage(websocket.PingMessage, []byte{}); err != nil {
				connInfo.conn.Close()
				connInfo.conn = nil
				break
			}
		}
	}(ticker)
	return nil
}

func findAvailConn(token string, uuid string) *connInfo {
	for _, value := range connPool[token] {
		if !value.lock {
			value.lock = true
			value.uuid = uuid
			return value
		}
	}
	newConnInfo := connInfo{uuid: uuid, lock: true}
	connPool[token] = append(connPool[token], &newConnInfo)
	return &newConnInfo
}
func findSpecConn(token string, uuid string) *connInfo {
	for _, value := range connPool[token] {
		if value.uuid == uuid {
			return value
		}
	}
	return &connInfo{}
}
func UnlockSpecConn(token string, uuid string) {
	for _, value := range connPool[token] {
		if value.uuid == uuid {
			value.lock = false
		}
	}
}
func InitWSConn(token string, uuid string, proxy string) error {
	connInfo := findAvailConn(token, uuid)
	conn := connInfo.conn
	isExpired := connInfo.expire.IsZero() || time.Now().After(connInfo.expire)
	if conn == nil || isExpired {
		if proxy != "" {
			client.SetProxy(proxy)
		}
		if conn != nil {
			connInfo.ticker.Stop()
			conn.Close()
			connInfo.conn = nil
		}
		wssURL, err := getWSURL(token, 0)
		if err != nil {
			return err
		}
		createWSConn(wssURL, connInfo, 0)
		if err != nil {
			return err
		}
		return nil
	} else {
		ctx, cancelFunc := context.WithTimeout(context.Background(), time.Millisecond*100)
		go func() {
			defer cancelFunc()
			for {
				_, _, err := conn.NextReader()
				if err != nil {
					break
				}
				if ctx.Err() != nil {
					break
				}
			}
		}()
		<-ctx.Done()
		err := ctx.Err()
		if err != nil {
			switch err {
			case context.Canceled:
				connInfo.ticker.Stop()
				conn.Close()
				connInfo.conn = nil
				connInfo.lock = false
				return InitWSConn(token, uuid, proxy)
			case context.DeadlineExceeded:
				return nil
			default:
				return nil
			}
		}
		return nil
	}
}

func POSTconversation(message chatgpt_types.ChatGPTRequest, access_token string, puid string, proxy string) (*http.Response, error) {
	if proxy != "" {
		client.SetProxy(proxy)
	}

	apiUrl := "https://chat.openai.com/backend-api/conversation"
	if API_REVERSE_PROXY != "" {
		apiUrl = API_REVERSE_PROXY
	}

	arkoseToken := message.ArkoseToken
	message.ArkoseToken = ""
	// JSONify the body and add it to the request
	body_json, err := json.Marshal(message)
	if err != nil {
		return &http.Response{}, err
	}

	request, err := http.NewRequest(http.MethodPost, apiUrl, bytes.NewBuffer(body_json))
	if err != nil {
		return &http.Response{}, err
	}
	// Clear cookies
	if puid != "" {
		request.Header.Set("Cookie", "_puid="+puid+";")
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/117.0.0.0 Safari/537.36")
	request.Header.Set("Accept", "text/event-stream")
	if arkoseToken != "" {
		request.Header.Set("Openai-Sentinel-Arkose-Token", arkoseToken)
	}
	if access_token != "" {
		request.Header.Set("Authorization", "Bearer "+access_token)
	}
	if err != nil {
		return &http.Response{}, err
	}
	response, err := client.Do(request)
	return response, err
}

// Returns whether an error was handled
func Handle_request_error(c *gin.Context, response *http.Response) bool {
	if response.StatusCode != 200 {
		// Try read response body as JSON
		var error_response map[string]interface{}
		err := json.NewDecoder(response.Body).Decode(&error_response)
		if err != nil {
			// Read response body
			body, _ := io.ReadAll(response.Body)
			c.JSON(500, gin.H{"error": gin.H{
				"message": "Unknown error",
				"type":    "internal_server_error",
				"param":   nil,
				"code":    "500",
				"details": string(body),
			}})
			return true
		}
		c.JSON(response.StatusCode, gin.H{"error": gin.H{
			"message": error_response["detail"],
			"type":    response.Status,
			"param":   nil,
			"code":    "error",
		}})
		return true
	}
	return false
}

type ContinueInfo struct {
	ConversationID string `json:"conversation_id"`
	ParentID       string `json:"parent_id"`
}

type fileInfo struct {
	DownloadURL string `json:"download_url"`
	Status      string `json:"status"`
}

func GetImageSource(wg *sync.WaitGroup, url string, prompt string, token string, puid string, idx int, imgSource []string) {
	defer wg.Done()
	request, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return
	}
	// Clear cookies
	if puid != "" {
		request.Header.Set("Cookie", "_puid="+puid+";")
	}
	request.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/117.0.0.0 Safari/537.36")
	request.Header.Set("Accept", "*/*")
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := client.Do(request)
	if err != nil {
		return
	}
	defer response.Body.Close()
	var file_info fileInfo
	err = json.NewDecoder(response.Body).Decode(&file_info)
	if err != nil || file_info.Status != "success" {
		return
	}
	imgSource[idx] = "[![image](" + file_info.DownloadURL + " \"" + prompt + "\")](" + file_info.DownloadURL + ")"
}

func Handler(c *gin.Context, response *http.Response, token string, puid string, uuid string, translated_request chatgpt_types.ChatGPTRequest, stream bool) (string, *ContinueInfo) {
	max_tokens := false

	// Create a bufio.Reader from the response body
	reader := bufio.NewReader(response.Body)

	// Read the response byte by byte until a newline character is encountered
	if stream {
		// Response content type is text/event-stream
		c.Header("Content-Type", "text/event-stream")
	} else {
		// Response content type is application/json
		c.Header("Content-Type", "application/json")
	}
	var finish_reason string
	var previous_text typings.StringStruct
	var original_response chatgpt_types.ChatGPTResponse
	var isRole = true
	var waitSource = false
	var isEnd = false
	var imgSource []string
	var isWSS = false
	var convId string
	var respId string
	//var wssUrl string
	var connInfo *connInfo
	var wsSeq int

	if !strings.Contains(response.Header.Get("Content-Type"), "text/event-stream") {
		isWSS = true
		connInfo = findSpecConn(token, uuid)
		if connInfo.conn == nil {
			c.JSON(500, gin.H{"error": "No websocket connection"})
			return "", nil
		}
		var wssResponse chatgpt_types.ChatGPTWSSResponse
		json.NewDecoder(response.Body).Decode(&wssResponse)
		//wssUrl = wssResponse.WssUrl
		respId = wssResponse.ResponseId
		convId = wssResponse.ConversationId
	}

	const readTimeout = 2 * time.Second
	for {
		var line string
		var err error
		if isWSS {
			//println("Reading message from WebSocket")
			if connInfo.conn != nil {
				connInfo.conn.SetReadDeadline(time.Now().Add(readTimeout))
			} else {
				// If the connection is nil, respond with an error immediately
				c.JSON(500, gin.H{"error": "WebSocket connection is closed or not initialized"})
				return "", nil
			}
			var messageType int
			var message []byte
			messageType, message, err = connInfo.conn.ReadMessage()
			if err != nil {
				if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
					println("Read timeout:", err.Error())
					connInfo.ticker.Stop()
					connInfo.conn.Close()
					connInfo.conn = nil
					c.JSON(500, gin.H{"error": "WebSocket read took too long"})
				} else {
					println(err.Error())
					connInfo.ticker.Stop()
					connInfo.conn.Close()
					connInfo.conn = nil
					c.JSON(500, gin.H{"error": err.Error()})
					return "", nil
				}
			}
			if messageType == websocket.TextMessage {
				var wssMsgResponse chatgpt_types.WSSMsgResponse
				json.Unmarshal(message, &wssMsgResponse)
				if wssMsgResponse.Data.ResponseId != respId {
					continue
				}
				wsSeq = wssMsgResponse.SequenceId
				if wsSeq%50 == 0 {
					connInfo.conn.WriteMessage(websocket.TextMessage, []byte("{\"type\":\"sequenceAck\",\"sequenceId\":"+strconv.Itoa(wsSeq)+"}"))
				}
				base64Body := wssMsgResponse.Data.Body
				bodyByte, err := base64.StdEncoding.DecodeString(base64Body)
				if err != nil {
					continue
				}
				line = string(bodyByte)
			}
		} else {
			line, err = reader.ReadString('\n')
			if err != nil {
				if err == io.EOF {
					break
				}
				return "", nil
			}
		}
		if len(line) < 6 {
			continue
		}
		// Remove "data: " from the beginning of the line
		line = line[6:]
		// Check if line starts with [DONE]
		if !strings.HasPrefix(line, "[DONE]") {
			// Parse the line as JSON
			err = json.Unmarshal([]byte(line), &original_response)
			if err != nil {
				continue
			}
			if original_response.Error != nil {
				c.JSON(500, gin.H{"error": original_response.Error})
				return "", nil
			}
			if original_response.ConversationID != convId {
				if convId == "" {
					convId = original_response.ConversationID
				} else {
					continue
				}
			}
			if !(original_response.Message.Author.Role == "assistant" || (original_response.Message.Author.Role == "tool" && original_response.Message.Content.ContentType != "text")) || original_response.Message.Content.Parts == nil {
				continue
			}
			if original_response.Message.Metadata.MessageType != "next" && original_response.Message.Metadata.MessageType != "continue" || !strings.HasSuffix(original_response.Message.Content.ContentType, "text") {
				continue
			}
			if original_response.Message.EndTurn != nil {
				if waitSource {
					waitSource = false
				}
				isEnd = true
			}
			if len(original_response.Message.Metadata.Citations) != 0 {
				r := []rune(original_response.Message.Content.Parts[0].(string))
				if waitSource {
					if string(r[len(r)-1:]) == "】" {
						waitSource = false
					} else {
						continue
					}
				}
				offset := 0
				for i, citation := range original_response.Message.Metadata.Citations {
					rl := len(r)
					original_response.Message.Content.Parts[0] = string(r[:citation.StartIx+offset]) + "[^" + strconv.Itoa(i+1) + "^](" + citation.Metadata.URL + " \"" + citation.Metadata.Title + "\")" + string(r[citation.EndIx+offset:])
					r = []rune(original_response.Message.Content.Parts[0].(string))
					offset += len(r) - rl
				}
			} else if waitSource {
				continue
			}
			response_string := ""
			if original_response.Message.Recipient != "all" {
				continue
			}
			if original_response.Message.Content.ContentType == "multimodal_text" {
				apiUrl := "https://chat.openai.com/backend-api/files/"
				if FILES_REVERSE_PROXY != "" {
					apiUrl = FILES_REVERSE_PROXY
				}
				imgSource = make([]string, len(original_response.Message.Content.Parts))
				var wg sync.WaitGroup
				for index, part := range original_response.Message.Content.Parts {
					jsonItem, _ := json.Marshal(part)
					var dalle_content chatgpt_types.DalleContent
					err = json.Unmarshal(jsonItem, &dalle_content)
					if err != nil {
						continue
					}
					url := apiUrl + strings.Split(dalle_content.AssetPointer, "//")[1] + "/download"
					wg.Add(1)
					go GetImageSource(&wg, url, dalle_content.Metadata.Dalle.Prompt, token, puid, index, imgSource)
				}
				wg.Wait()
				translated_response := official_types.NewChatCompletionChunk(strings.Join(imgSource, ""))
				if isRole {
					translated_response.Choices[0].Delta.Role = original_response.Message.Author.Role
				}
				response_string = "data: " + translated_response.String() + "\n\n"
			}
			if response_string == "" {
				response_string = chatgpt_response_converter.ConvertToString(&original_response, &previous_text, isRole)
			}
			if response_string == "" {
				if isEnd {
					goto endProcess
				} else {
					continue
				}
			}
			if response_string == "【" {
				waitSource = true
				continue
			}
			isRole = false
			if stream {
				_, err = c.Writer.WriteString(response_string)
				if err != nil {
					return "", nil
				}
			}
		endProcess:
			// Flush the response writer buffer to ensure that the client receives each line as it's written
			c.Writer.Flush()

			if original_response.Message.Metadata.FinishDetails != nil {
				if original_response.Message.Metadata.FinishDetails.Type == "max_tokens" {
					max_tokens = true
				}
				finish_reason = original_response.Message.Metadata.FinishDetails.Type
			}
			if isEnd {
				if stream {
					final_line := official_types.StopChunk(finish_reason)
					c.Writer.WriteString("data: " + final_line.String() + "\n\n")
				}
				break
			}
		}
	}
	if !max_tokens {
		return strings.Join(imgSource, "") + previous_text.Text, nil
	}
	return strings.Join(imgSource, "") + previous_text.Text, &ContinueInfo{
		ConversationID: original_response.ConversationID,
		ParentID:       original_response.Message.ID,
	}
}
