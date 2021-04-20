package main

import (
	"fmt"
	"io/ioutil"
	"net/http"
	"os"
	"strings"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/route53"
	"github.com/cloudflare/cloudflare-go"
	"github.com/hashicorp/consul/api"
	"github.com/prometheus/common/log"
	"github.com/urfave/cli/v2"
)

type (
	// DNSRecord represents needed info to update or create a DNS Record
	DNSRecord struct {
		ID          string
		Type        string
		Content     string
		Proxied     bool
		Name        string
		Destination string
		TTL         int
		Local       bool
	}
)

var (
	consulURL       string
	cloudflareToken string
	zoneID          string
	dryRun          bool
	region          string

	DNSProvider string

	publicIP string
)

func main() {
	app := &cli.App{
		Name:  "boom",
		Usage: "make an explosive entrance",
		Action: func(c *cli.Context) error {
			DNSProvider = "cloudflare"
			if cloudflareToken == "" {
				DNSProvider = "route53"
			}

			publicIP = getPublicIP()
			existingZoneRecords := getZoneRecords(zoneID)
			if existingZoneRecords == nil {
				return fmt.Errorf("failed to retrieve zone records")
			}

			config := api.DefaultConfig()
			config.Address = consulURL
			client, err := api.NewClient(config)
			if err != nil {
				log.Error(err)
				panic(err)
			}

			catalog := client.Catalog()

			services, _, err := catalog.Services(&api.QueryOptions{})
			if err != nil {
				log.Error(err)
				return err
			}

			for service, tags := range services {
				settings := getSettings(tags)
				if len(settings) > 0 {
					record := DNSRecord{
						Type: "A",
						TTL:  1,
					}
					for _, setting := range settings {
						settingPieces := strings.Split(setting, "=")
						if strings.HasPrefix(settingPieces[0], "wayfinder.domain") {
							record.Name = settingPieces[1]
						} else if strings.HasPrefix(settingPieces[0], "wayfinder.public") && settingPieces[1] == strings.ToLower("true") {
							record.Content = publicIP
							record.Proxied = true
						} else if strings.HasPrefix(settingPieces[0], "wayfinder.address") {
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
						log.Info("records Don't match")
						log.Infof("%#v\n", currentRecord)
						log.Infof("%#v\n", record)
						if !dryRun {
							updateZoneRecord(record, zoneID)
						}
					} else {
						log.Info("Record matches no need to update: ", service)
					}
				}
			}
			return nil
		},
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:        "region",
				Value:       "us-west-2",
				Destination: &region,
				EnvVars: []string{
					"AWS_REGION",
				},
				Aliases: []string{},
			},
			&cli.BoolFlag{
				Name:        "dry-run",
				Value:       false,
				Destination: &dryRun,
				EnvVars:     []string{},
				Aliases:     []string{},
			},
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
				Required:    false,
				EnvVars: []string{
					"CLOUDFLARE_API_TOKEN",
				},
			},
			&cli.StringFlag{
				Name:        "zone-id",
				Destination: &zoneID,
				Required:    true,
				EnvVars: []string{
					"CLOUDFLARE_ZONE_ID",
					"ZONE_ID",
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

func getZoneRecords(zoneID string) map[string]DNSRecord {
	result := map[string]DNSRecord{}
	if DNSProvider == "route53" {
		sess, err := session.NewSession(aws.NewConfig().WithRegion(region))
		if err != nil {
			log.Errorf("failed to instantiate a session: %+v", err)
			return nil
		}

		svc := route53.New(sess)
		out, err := svc.ListResourceRecordSets(&route53.ListResourceRecordSetsInput{
			HostedZoneId: aws.String(zoneID),
		})
		if err != nil {
			log.Error(err)
		}

		for _, record := range out.ResourceRecordSets {
			contents := []string{}
			for _, content := range record.ResourceRecords {
				contents = append(contents, *content.Value)
			}

			result[strings.TrimSuffix(*record.Name, ".")] = DNSRecord{
				Name:    *record.Name,
				TTL:     int(*record.TTL),
				Type:    *record.Type,
				Content: strings.Join(contents, ", "),
			}
		}

		return result

	} else {
		cf, err := cloudflare.NewWithAPIToken(cloudflareToken)
		if err != nil {
			log.Errorf("Failed to create new cloudflare client: %+v", err)
			return nil
		}

		records, err := cf.DNSRecords(zoneID, cloudflare.DNSRecord{Type: "A"})
		if err != nil {
			log.Errorf("Failed to query zone for existing records: %+v", err)
			return nil
		}

		for _, record := range records {
			result[record.Name] = DNSRecord{
				ID:      record.ID,
				Name:    record.Name,
				TTL:     record.TTL,
				Type:    record.Type,
				Content: record.Content,
			}
		}

		return result

	}
}

func updateZoneRecord(record DNSRecord, zoneID string) {
	if DNSProvider == "route53" {
		sess, err := session.NewSession(aws.NewConfig().WithRegion(region))
		if err != nil {
			log.Errorf("failed to instantiate a session: %+v", err)
			return
		}

		svc := route53.New(sess)
		resourceRecords := []*route53.ResourceRecord{}

		for _, resRecord := range strings.Split(record.Content, ", ") {
			resourceRecords = append(resourceRecords, &route53.ResourceRecord{
				Value: aws.String(resRecord),
			})
		}
		input := &route53.ChangeResourceRecordSetsInput{
			ChangeBatch: &route53.ChangeBatch{
				Changes: []*route53.Change{
					{
						Action: aws.String("UPSERT"),
						ResourceRecordSet: &route53.ResourceRecordSet{
							Name:            aws.String(record.Name),
							ResourceRecords: resourceRecords,
							TTL:             aws.Int64(int64(record.TTL)),
							Type:            aws.String(record.Type),
						},
					},
				},
				Comment: aws.String("Wayfinder Managed Domain"),
			},
			HostedZoneId: aws.String(zoneID),
		}

		result, err := svc.ChangeResourceRecordSets(input)
		if err != nil {
			log.Error(err)
			return
		}

		log.Info(result)

	} else if DNSProvider == "cloudflare" {
		if record.Name != "" {
			log.Info("Delete Cloudflare Record: ", record.Name)
			if !dryRun {
				deleteCloudflareZoneRecord(record.ID, zoneID)
			}
		}
		log.Info("Create Cloudflare Record: ", record)
		if !dryRun {
			createCloudflareZoneRecord(record, zoneID)
		}
	}
}

func createCloudflareZoneRecord(record DNSRecord, zoneID string) {
	cf, err := cloudflare.NewWithAPIToken(cloudflareToken)
	if err != nil {
		fmt.Println(err)
		return
	}

	res, err := cf.CreateDNSRecord(zoneID, cloudflare.DNSRecord{
		Type:    record.Type,
		TTL:     record.TTL,
		Proxied: record.Proxied,
		Content: record.Content,
		Name:    record.Name,
	})
	if err != nil {
		log.Error(err)
		return
	}

	log.Info(res)
}

func deleteCloudflareZoneRecord(id, zoneID string) {
	cf, err := cloudflare.NewWithAPIToken(cloudflareToken)
	if err != nil {
		log.Error(err)
		return
	}

	cf.DeleteDNSRecord(zoneID, id)
}
