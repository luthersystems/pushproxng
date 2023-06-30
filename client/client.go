package main

import (
	"bufio"
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/luthersystems/pushproxng/common"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

const (
	mimeTypeJSON = "application/json"
)

func run() {
	viper.SetEnvPrefix("pushproxng_client")
	viper.SetEnvKeyReplacer(strings.NewReplacer("-", "_"))
	_ = viper.BindEnv("proxy")
	_ = viper.BindEnv("fqdn")
	_ = viper.BindEnv("target")

	proxy := viper.GetString("proxy")
	fqdn := viper.GetString("fqdn")
	target := viper.GetString("target")

	log := (func(err error) {
		fmt.Printf("encountered: %s\n", err)
	})

	pause := (func() {
		time.Sleep(30 * time.Second)
	})

	launched := time.Now().Unix()

	for {
		ag1 := &common.PollRequest{FQDN: fqdn, Launched: int(launched)}

		inp1, err := json.Marshal(ag1)
		if err != nil {
			log(err)
			break
		}

		respPoll, err := http.Post(fmt.Sprintf("http://%s/poll", proxy), mimeTypeJSON, bytes.NewReader(inp1))
		if err != nil {
			log(err)
			pause()
			continue
		}
		respPollBytes, err := io.ReadAll(respPoll.Body)
		respPoll.Body.Close()
		if err != nil {
			log(err)
			pause()
			continue
		}

		var ag2 common.PollReply
		err = json.Unmarshal(respPollBytes, &ag2)
		if err != nil {
			log(err)
			pause()
			continue
		}

		requestBytes, err := hex.DecodeString(ag2.Request)
		if err != nil {
			log(err)
			pause()
			continue
		}

		request, err := http.ReadRequest(bufio.NewReader(bytes.NewReader(requestBytes)))
		if err != nil {
			log(err)
			pause()
			continue
		}

		request.URL.Scheme = "http"
		request.URL.Host = target
		request.RequestURI = ""

		response, err := (&http.Client{}).Do(request)
		if err != nil {
			log(err)
			pause()
			continue
		}

		var responseBytes bytes.Buffer
		err = response.Write(&responseBytes)
		if err != nil {
			log(err)
			pause()
			continue
		}

		ag3 := &common.PushRequest{
			FQDN:     fqdn,
			UUID:     ag2.UUID,
			Response: hex.EncodeToString(responseBytes.Bytes()),
		}

		inp3, err := json.Marshal(ag3)
		if err != nil {
			log(err)
			break
		}

		respPush, err := http.Post(fmt.Sprintf("http://%s/push", proxy), mimeTypeJSON, bytes.NewReader(inp3))
		if err != nil {
			log(err)
			pause()
			continue
		}
		respPush.Body.Close()
	}

	os.Exit(1)
}

var cfgFile string

var rootCmd = &cobra.Command{
	Use:   "client",
	Short: "Pushproxng's client command",
	Long:  "Pushproxng's client command",
	Run: func(cmd *cobra.Command, args []string) {
		run()
	},
}

func Execute() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
}

func init() {
	cobra.OnInitialize(initConfig)

	rootCmd.PersistentFlags().StringVar(&cfgFile, "config", "", "config file")

	rootCmd.Flags().String("proxy", "invalid", "Proxy address")
	_ = viper.BindPFlag("proxy", rootCmd.Flags().Lookup("proxy"))

	rootCmd.Flags().String("fqdn", "invalid", "FQDN")
	_ = viper.BindPFlag("fqdn", rootCmd.Flags().Lookup("fqdn"))

	rootCmd.Flags().String("target", "invalid", "Target address")
	_ = viper.BindPFlag("target", rootCmd.Flags().Lookup("target"))
}

func initConfig() {
	if cfgFile != "" {
		viper.SetConfigFile(cfgFile)
	}

	if err := viper.ReadInConfig(); err == nil {
		fmt.Println("Using config file:", viper.ConfigFileUsed())
	}
}

func main() {
	Execute()
}
