package main

import (
	"io/ioutil"
	"os"
	"path/filepath"
	"sync"

	"github.com/AdguardTeam/AdGuardHome/dhcpd"
	"github.com/AdguardTeam/AdGuardHome/dnsfilter"
	"github.com/AdguardTeam/AdGuardHome/dnsforward"
	"github.com/hmage/golibs/log"
	yaml "gopkg.in/yaml.v2"
)

const (
	dataDir   = "data"    // data storage
	filterDir = "filters" // cache location for downloaded filters, it's under DataDir
)

// logSettings
type logSettings struct {
	LogFile string `yaml:"log_file"` // Path to the log file. If empty, write to stdout. If "syslog", writes to syslog
	Verbose bool   `yaml:"verbose"`  // If true, verbose logging is enabled
}

// configuration is loaded from YAML
// field ordering is important -- yaml fields will mirror ordering from here
type configuration struct {
	ourConfigFilename string // Config filename (can be overridden via the command line arguments)
	ourBinaryDir      string // Location of our directory, used to protect against CWD being somewhere else

	BindHost  string             `yaml:"bind_host"`
	BindPort  int                `yaml:"bind_port"`
	AuthName  string             `yaml:"auth_name"`
	AuthPass  string             `yaml:"auth_pass"`
	Language  string             `yaml:"language"` // two-letter ISO 639-1 language code
	DNS       dnsConfig          `yaml:"dns"`
	Filters   []filter           `yaml:"filters"`
	UserRules []string           `yaml:"user_rules"`
	DHCP      dhcpd.ServerConfig `yaml:"dhcp"`

	logSettings `yaml:",inline"`

	sync.RWMutex `yaml:"-"`

	SchemaVersion int `yaml:"schema_version"` // keeping last so that users will be less tempted to change it -- used when upgrading between versions
}

// field ordering is important -- yaml fields will mirror ordering from here
type dnsConfig struct {
	BindHost string `yaml:"bind_host"`
	Port     int    `yaml:"port"`

	dnsforward.FilteringConfig `yaml:",inline"`

	UpstreamDNS []string `yaml:"upstream_dns"`
}

var defaultDNS = []string{"tls://1.1.1.1", "tls://1.0.0.1"}

// initialize to default values, will be changed later when reading config or parsing command line
var config = configuration{
	ourConfigFilename: "AdGuardHome.yaml",
	BindPort:          3000,
	BindHost:          "127.0.0.1",
	DNS: dnsConfig{
		BindHost: "0.0.0.0",
		Port:     53,
		FilteringConfig: dnsforward.FilteringConfig{
			ProtectionEnabled:  true, // whether or not use any of dnsfilter features
			FilteringEnabled:   true, // whether or not use filter lists
			BlockedResponseTTL: 10,   // in seconds
			QueryLogEnabled:    true,
			Ratelimit:          20,
			RefuseAny:          true,
			BootstrapDNS:       "8.8.8.8:53",
		},
		UpstreamDNS: defaultDNS,
	},
	Filters: []filter{
		{Filter: dnsfilter.Filter{ID: 1}, Enabled: true, URL: "https://adguardteam.github.io/AdGuardSDNSFilter/Filters/filter.txt", Name: "AdGuard Simplified Domain Names filter"},
		{Filter: dnsfilter.Filter{ID: 2}, Enabled: false, URL: "https://adaway.org/hosts.txt", Name: "AdAway"},
		{Filter: dnsfilter.Filter{ID: 3}, Enabled: false, URL: "https://hosts-file.net/ad_servers.txt", Name: "hpHosts - Ad and Tracking servers only"},
		{Filter: dnsfilter.Filter{ID: 4}, Enabled: false, URL: "http://www.malwaredomainlist.com/hostslist/hosts.txt", Name: "MalwareDomainList.com Hosts List"},
	},
	SchemaVersion: currentSchemaVersion,
}

// getConfigFilename returns path to the current config file
func (c *configuration) getConfigFilename() string {
	configFile := config.ourConfigFilename
	if !filepath.IsAbs(configFile) {
		configFile = filepath.Join(config.ourBinaryDir, config.ourConfigFilename)
	}
	return configFile
}

// getLogSettings reads logging settings from the config file.
// we do it in a separate method in order to configure logger before the actual configuration is parsed and applied.
func getLogSettings() logSettings {
	l := logSettings{}
	yamlFile, err := readConfigFile()
	if err != nil || yamlFile == nil {
		return l
	}
	err = yaml.Unmarshal(yamlFile, &l)
	if err != nil {
		log.Printf("Couldn't get logging settings from the configuration: %s", err)
	}
	return l
}

// parseConfig loads configuration from the YAML file
func parseConfig() error {
	configFile := config.getConfigFilename()
	log.Printf("Reading YAML file: %s", configFile)
	yamlFile, err := readConfigFile()
	if err != nil {
		log.Printf("Couldn't read config file: %s", err)
		return err
	}
	if yamlFile == nil {
		log.Printf("YAML file doesn't exist, skipping it")
		return nil
	}
	err = yaml.Unmarshal(yamlFile, &config)
	if err != nil {
		log.Printf("Couldn't parse config file: %s", err)
		return err
	}

	// Deduplicate filters
	deduplicateFilters()

	updateUniqueFilterID(config.Filters)

	return nil
}

// readConfigFile reads config file contents if it exists
func readConfigFile() ([]byte, error) {
	configFile := config.getConfigFilename()
	if _, err := os.Stat(configFile); os.IsNotExist(err) {
		// do nothing, file doesn't exist
		return nil, nil
	}
	return ioutil.ReadFile(configFile)
}

// Saves configuration to the YAML file and also saves the user filter contents to a file
func (c *configuration) write() error {
	c.Lock()
	defer c.Unlock()
	configFile := config.getConfigFilename()
	log.Printf("Writing YAML file: %s", configFile)
	yamlText, err := yaml.Marshal(&config)
	if err != nil {
		log.Printf("Couldn't generate YAML file: %s", err)
		return err
	}
	err = safeWriteFile(configFile, yamlText)
	if err != nil {
		log.Printf("Couldn't save YAML config: %s", err)
		return err
	}

	return nil
}

func writeAllConfigs() error {
	err := config.write()
	if err != nil {
		log.Printf("Couldn't write config: %s", err)
		return err
	}

	userFilter := userFilter()
	err = userFilter.save()
	if err != nil {
		log.Printf("Couldn't save the user filter: %s", err)
		return err
	}

	return nil
}
