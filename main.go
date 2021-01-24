package main

//go:generate go run ./scripts/generate_certs.go ./ca.crt ./private-key.pem ./certs.go

import (
	"bytes"
	"crypto/sha1" // nolint: gosec
	"fmt"
	"io/ioutil"
	"net"
	"os"

	log "github.com/sirupsen/logrus"
	kingpin "gopkg.in/alecthomas/kingpin.v2"

	"github.com/zytegroup/zyte-proxy-headless-proxy/config"
	"github.com/zytegroup/zyte-proxy-headless-proxy/proxy"
	"github.com/zytegroup/zyte-proxy-headless-proxy/stats"
)

var version = "dev" // nolint: gochecknoglobals

var ( // nolint: gochecknoglobals
	app = kingpin.New("zyte-proxy-headless-proxy",
		"Local proxy for Zyte Smart Proxy Manager to be used with headless browsers.")

	debug = app.Flag("debug",
		"Run in debug mode.").
		Short('d').
		Envar("ZYTE_PROXY_HEADLESS_DEBUG").
		Bool()
	bindIP = app.Flag("bind-ip",
		"IP to bind to. Default is 127.0.0.1.").
		Short('b').
		Envar("ZYTE_PROXY_HEADLESS_BINDIP").
		IP()
	proxyAPIIP = app.Flag("proxy-api-ip",
		"IP to bind proxy API to. Default is the bind-ip value.").
		Short('m').
		Envar("ZYTE_PROXY_HEADLESS_PROXYAPIIP").
		IP()
	bindPort = app.Flag("bind-port",
		"Port to bind to. Default is 3128.").
		Short('p').
		Envar("ZYTE_PROXY_HEADLESS_BINDPORT").
		Int()
	proxyAPIPort = app.Flag("proxy-api-port",
		"Port to bind proxy api to. Default is 3130.").
		Short('w').
		Envar("ZYTE_PROXY_HEADLESS_PROXYAPIPORT").
		Int()
	configFileName = app.Flag("config",
		"Path to configuration file.").
		Short('c').
		Envar("ZYTE_PROXY_HEADLESS_CONFIG").
		File()
	tlsCaCertificate = app.Flag("tls-ca-certificate",
		"Path to TLS CA certificate file.").
		Short('l').
		Envar("ZYTE_PROXY_HEADLESS_TLSCACERTPATH").
		ExistingFile()
	tlsPrivateKey = app.Flag("tls-private-key",
		"Path to TLS private key.").
		Short('r').
		Envar("ZYTE_PROXY_HEADLESS_TLSPRIVATEKEYPATH").
		ExistingFile()
	noAutoSessions = app.Flag("no-auto-sessions",
		"Disable automatic session management.").
		Short('t').
		Envar("ZYTE_PROXY_HEADLESS_NOAUTOSESSIONS").
		Bool()
	concurrentConnections = app.Flag("concurrent-connections",
		"Number of concurrent connections.").
		Short('n').
		Envar("ZYTE_PROXY_HEADLESS_CONCURRENCY").
		Int()
	apiKey = app.Flag("api-key",
		"API key to Zyte Smart Proxy Manager.").
		Short('a').
		Envar("ZYTE_PROXY_HEADLESS_APIKEY").
		String()
	zyteProxyHost = app.Flag("zyte-proxy-host",
		"Hostname of Zyte Smart Proxy Manager. Default is proxy.zyte.com.").
		Short('u').
		Envar("ZYTE_PROXY_HEADLESS_ZYTEPROXYHOST").
		String()
	zyteProxyPort = app.Flag("zyte-proxy-port",
		"Port of Zyte Smart Proxy Manager. Default is 8010.").
		Short('o').
		Envar("ZYTE_PROXY_HEADLESS_ZYTEPROXYPORT").
		Int()
	doNotVerifyZyteProxyCert = app.Flag("dont-verify-zyte-proxy-cert",
		"Do not verify Zyte Smart Proxy Manager own certificate.").
		Short('v').
		Envar("ZYTE_PROXY_HEADLESS_DONTVERIFY").
		Bool()
	zyteProxyHeaders = app.Flag("zyte-proxy-header",
		"Zyte Smart Proxy Manager headers.").
		Short('x').
		Envar("ZYTE_PROXY_HEADLESS_ZYTEPROXYHEADERS").
		StringMap()
	adblockLists = app.Flag("adblock-list",
		"A list to requests to filter out (ADBlock compatible).").
		Short('k').
		Envar("ZYTE_PROXY_HEADLESS_ADBLOCKLISTS").
		Strings()
	directAccessHostPathRegexps = app.Flag("direct-access-hostpath-regexps",
		"A list of regexps for hostpath for direct access, bypassing Zyte Smart Proxy Manager.").
		Short('z').
		Envar("ZYTE_PROXY_HEADLESS_DIRECTACCESS").
		Strings()
)

func main() {
	app.Version(version)
	log.SetFormatter(&log.TextFormatter{})
	log.SetLevel(log.WarnLevel)

	kingpin.MustParse(app.Parse(os.Args[1:]))

	conf, err := getConfig()
	if err != nil {
		log.Errorf("Cannot get configuration: %s", err)
		os.Exit(1)
	}

	if conf.Debug {
		log.SetLevel(log.DebugLevel)
	}

	if conf.APIKey == "" {
		log.Fatal("API key is not set")
	}

	if err = initCertificates(conf); err != nil {
		log.Fatal(err)
	}

	listen := conf.Bind()
	log.WithFields(log.Fields{
		"debug":                          conf.Debug,
		"adblock-lists":                  conf.AdblockLists,
		"no-auto-sessions":               conf.NoAutoSessions,
		"apikey":                         conf.APIKey,
		"bindip":                         conf.BindIP,
		"bindport":                       conf.BindPort,
		"proxy-api-ip":                   conf.ProxyAPIIP,
		"proxy-api-port":                 conf.ProxyAPIPort,
		"zyte-proxy-host":                conf.ZyteProxyHost,
		"zyte-proxy-port":                conf.ZyteProxyPort,
		"dont-verify-zyte-proxy-cert":    conf.DoNotVerifyZyteProxyCert,
		"concurrent-connections":         conf.ConcurrentConnections,
		"zyte-proxy-headers":             conf.ZyteProxyHeaders,
		"direct-access-hostpath-regexps": conf.DirectAccessHostPathRegexps,
	}).Debugf("Listen on %s", listen)

	statsContainer := stats.NewStats()

	go stats.RunStats(statsContainer, conf)

	if zyteProxyProxy, err := proxy.NewProxy(conf, statsContainer); err == nil {
		if ln, err2 := net.Listen("tcp", listen); err2 != nil {
			log.Fatal(err2)
		} else {
			log.Fatal(zyteProxyProxy.Serve(ln))
		}
	} else {
		log.Fatal(err)
	}
}

func getConfig() (*config.Config, error) {
	conf := config.NewConfig()

	if *configFileName != nil {
		newConf, err := config.Parse(*configFileName)
		if err != nil {
			return nil, err
		}

		conf = newConf
	}

	conf.MaybeSetDebug(*debug)
	conf.MaybeDoNotVerifyZyteProxyCert(*doNotVerifyZyteProxyCert)
	conf.MaybeSetAdblockLists(*adblockLists)
	conf.MaybeSetAPIKey(*apiKey)
	conf.MaybeSetBindIP(*bindIP)
	conf.MaybeSetBindPort(*bindPort)
	conf.MaybeSetConcurrentConnections(*concurrentConnections)
	conf.MaybeSetZyteProxyHost(*zyteProxyHost)
	conf.MaybeSetZyteProxyPort(*zyteProxyPort)
	conf.MaybeSetNoAutoSessions(*noAutoSessions)
	conf.MaybeSetTLSCaCertificate(*tlsCaCertificate)
	conf.MaybeSetTLSPrivateKey(*tlsPrivateKey)
	conf.MaybeSetProxyAPIIP(*proxyAPIIP)
	conf.MaybeSetProxyAPIPort(*proxyAPIPort)
	conf.MaybeSetDirectAccessHostPathRegexps(*directAccessHostPathRegexps)

	for k, v := range *zyteProxyHeaders {
		conf.SetXHeader(k, v)
	}

	if conf.ProxyAPIIP == "" {
		conf.ProxyAPIIP = conf.BindIP
	}

	return conf, nil
}

func initCertificates(conf *config.Config) (err error) {
	caCertificate := DefaultCertCA
	privateKey := DefaultPrivateKey

	if conf.TLSCaCertificate != "" {
		caCertificate, err = ioutil.ReadFile(conf.TLSCaCertificate)
		if err != nil {
			return fmt.Errorf("cannot read TLS CA certificate: %w", err)
		}
	}

	if conf.TLSPrivateKey != "" {
		privateKey, err = ioutil.ReadFile(conf.TLSPrivateKey)
		if err != nil {
			return fmt.Errorf("cannot read TLS private key: %w", err)
		}
	}

	conf.TLSCaCertificate = string(bytes.TrimSpace(caCertificate))
	conf.TLSPrivateKey = string(bytes.TrimSpace(privateKey))

	log.WithFields(log.Fields{
		"ca-cert":  fmt.Sprintf("%x", sha1.Sum([]byte(conf.TLSCaCertificate))), // nolint: gosec
		"priv-key": fmt.Sprintf("%x", sha1.Sum([]byte(conf.TLSPrivateKey))),    // nolint: gosec
	}).Debug("TLS checksums.")

	return nil
}
