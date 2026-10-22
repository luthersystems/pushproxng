package main

import (
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/luthersystems/pushproxng/internal/agent"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
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

	_ = agent.Run(proxy, fqdn, func(request *http.Request) (*http.Response, error) {
		request.URL.Scheme = "http"
		request.URL.Host = target
		request.RequestURI = ""
		return http.DefaultClient.Do(request)
	})

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
