package main

import (
	"fmt"
	"io/ioutil"
	"net/http"
	"os"
	"strings"

	"github.com/cloudflare/cloudflare-go"
	"github.com/hashicorp/consul/api"
	"github.com/urfave/cli/v2"
)

type (
	// DNSRecord represents needed info to update or create a DNS Record
	DNSRecord struct {
		Type        string
		Name        string
		Destination string
		TTL         int
		Local       bool
	}
)

var (
	consulURL        string
	cloudflareToken  string
	cloudflareZoneID string

	cloudflareBaseURL = "https://api.cloudflare.com/client/v4/"

	publicIP string
)

func main() {
	app := &cli.App{
		Name:  "boom",
		Usage: "make an explosive entrance",
		Action: func(c *cli.Context) error {
			publicIP = getPublicIP()
			existingZoneRecords := getZoneRecords(cloudflareZoneID)

			config := api.DefaultConfig()
			config.Address = consulURL
			client, err := api.NewClient(config)
			if err != nil {
				panic(err)
			}

			catalog := client.Catalog()

			services, _, err := catalog.Services(&api.QueryOptions{})
			if err != nil {
				return err
			}

			for service, tags := range services {
				settings := getSettings(tags)
				if len(settings) > 0 {
					record := cloudflare.DNSRecord{
						Type: "A",
						TTL:  1,
					}
					for _, setting := range settings {
						settingPieces := strings.Split(setting, "=")
						if settingPieces[0] == "wayfinder.domain" {
							record.Name = settingPieces[1]
						} else if settingPieces[0] == "wayfinder.public" && settingPieces[1] == strings.ToLower("true") {
							record.Content = publicIP
							record.Proxied = true
						} else if settingPieces[0] == "wayfinder.address" {
							record.Content = settingPieces[1]
						}
					}
					if record.Content == "" {
						service, _, _ := catalog.Service(service, "", &api.QueryOptions{})
						record.Content = service[0].Address
						record.Proxied = false
					}

					currentRecord := existingZoneRecords[record.Name]

					if currentRecord.Name == "" || currentRecord.Content != record.Content || currentRecord.TTL != record.TTL || currentRecord.Type != record.Type {
						fmt.Println("records Don't match")
						fmt.Printf("%#v\n", currentRecord)
						fmt.Printf("%#v\n", record)
						if currentRecord.Name != "" {
							fmt.Println("Delete Record: ", currentRecord.ID)
							deleteZoneRecord(currentRecord.ID, cloudflareZoneID)
						}
						fmt.Println("Create Record: ", record)
						createZoneRecord(record, cloudflareZoneID)
					} else {
						fmt.Println("Record matches no need to update: ", service)
					}
				}
			}
			return nil
		},
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:        "consul-url",
				Value:       "http://consul.service.consul:8500",
				Destination: &consulURL,
				EnvVars: []string{
					"CONSUL_URL",
				},
			},
			&cli.StringFlag{
				Name:        "cloudflare-api-key",
				Destination: &cloudflareToken,
				Required:    true,
				EnvVars: []string{
					"CLOUDFLARE_API_TOKEN",
				},
			},
			&cli.StringFlag{
				Name:        "cloudflare-zone-id",
				Destination: &cloudflareZoneID,
				Required:    true,
				EnvVars: []string{
					"CLOUDFLARE_ZONE_ID",
				},
			},
		},
		Commands: []*cli.Command{
			{
				Name:  "sync",
				Flags: []cli.Flag{},
				Action: func(c *cli.Context) error {

					return nil
				},
			},
		},
	}

	app.Run(os.Args)
}

func getSettings(tags []string) []string {
	result := []string{}
	for _, tag := range tags {
		if strings.HasPrefix(tag, "wayfinder") {
			result = append(result, tag)
		}
	}
	return result
}

func getPublicIP() string {
	client := http.Client{}

	req, _ := http.NewRequest("GET", "https://icanhazip.com", nil)

	res, _ := client.Do(req)

	body, _ := ioutil.ReadAll(res.Body)

	return strings.TrimSpace(string(body))
}

func getZoneRecords(zoneID string) map[string]cloudflare.DNSRecord {
	cf, err := cloudflare.NewWithAPIToken(cloudflareToken)
	if err != nil {
		fmt.Println(err)
		return nil
	}

	records, err := cf.DNSRecords(zoneID, cloudflare.DNSRecord{Type: "A"})
	if err != nil {
		fmt.Println(err)
		return nil
	}

	result := map[string]cloudflare.DNSRecord{}
	for _, record := range records {
		fmt.Println(record.Name)
		result[record.Name] = record
	}

	return result
}

func createZoneRecord(record cloudflare.DNSRecord, zoneID string) {
	cf, err := cloudflare.NewWithAPIToken(cloudflareToken)
	if err != nil {
		fmt.Println(err)
		return
	}

	res, err := cf.CreateDNSRecord(zoneID, record)
	if err != nil {
		fmt.Println(err)
		return
	}

	fmt.Println(res)
}

func deleteZoneRecord(id, zoneID string) {
	cf, err := cloudflare.NewWithAPIToken(cloudflareToken)
	if err != nil {
		fmt.Println(err)
		// return nil
	}

	cf.DeleteDNSRecord(zoneID, id)
}
