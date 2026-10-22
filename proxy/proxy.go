package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/luthersystems/pushproxng/internal/proxyserver"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

func run() {
	viper.SetEnvPrefix("pushproxng_proxy")
	viper.SetEnvKeyReplacer(strings.NewReplacer("-", "_"))
	_ = viper.BindEnv("listen")

	listenAddress := viper.GetString("listen")

	s := proxyserver.NewState()
	proxyserver.Start(context.Background(), s)

	if err := proxyserver.NewServer(listenAddress, s).ListenAndServe(); err != nil {
		fmt.Printf("encountered: %s\n", err)
		os.Exit(1)
	}
}

var cfgFile string

var rootCmd = &cobra.Command{
	Use:   "proxy",
	Short: "Pushproxng's proxy command",
	Long:  "Pushproxng's proxy command",
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

	rootCmd.Flags().String("listen", ":8080", "Listen address")
	_ = viper.BindPFlag("listen", rootCmd.Flags().Lookup("listen"))
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
