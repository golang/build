// Copyright 2015 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// The scaleway command creates ARM servers on Scaleway.com.
package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/md5"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"go4.org/types"
	"golang.org/x/build/internal/secret"
	revtype "golang.org/x/build/types"
)

var (
	tokenDir    = flag.String("token-dir", filepath.Join(os.Getenv("HOME"), "keys"), "directory to read gobuilder-staging.key, gobuilder-master.key and go-scaleway.token from.")
	token       = flag.String("token", "", "API token. If empty, the file is read from $(token-dir)/go-scaleway.token. Googlers on the Go team can get the value from http://go/golang-scaleway-token")
	org         = flag.String("org", "1f34701d-668b-441b-bf08-0b13544e99de", "Organization ID (default is bradfitz@golang.org's account)")
	image       = flag.String("image", "13f4c905-3a4b-475a-aaba-a13168e2b6c7", "Disk image ID; default is the snapshot we made last")
	bootscript  = flag.String("bootscript", "5c8e4527-d166-4844-b6c6-087d7a6f5fb0", "Bootscript ID; empty means to use the default for the image. But our images don't have a correct default.")
	num         = flag.Int("n", 0, "Number of servers to create; if zero, defaults to a value as a function of --staging")
	tags        = flag.String("tags", "", "Comma-separated list of tags. The build key tags should be of the form 'buildkey_linux-arm_HEXHEXHEXHEXHEX'. If empty, it's automatic.")
	staging     = flag.Bool("staging", false, "If true, deploy staging instances (with staging names and tags) instead of prod.")
	listAll     = flag.Bool("list-all", false, "If true, list all (prod, staging, other) current Scaleway servers and stop without making changes.")
	list        = flag.Bool("list", false, "If true, list all prod (or staging, if -staging) servers, including missing ones.")
	fixInterval = flag.Duration("fix-interval", 10*time.Minute, "Interval to wait before running again (only applies to daemon mode)")
	daemonMode  = flag.Bool("daemon", false, "Run in daemon mode in a loop")
	ipv6        = flag.Bool("ipv6", false, "enable IPv6 on scaleway instances")
)

const (
	// ctype is the Commercial Type of server we use for the builders.
	ctype = "C1"

	scalewayAPIBase = "https://api.scaleway.com"
)

func main() {
	flag.Parse()

	secretClient := mustCreateSecretClient()
	defer secretClient.Close()

	if *tags == "" && !*listAll { // Tags aren't needed if -list-all flag is set.
		if *staging {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			key, err := secretClient.Retrieve(ctx, "builders_staging_key")
			if err != nil {
				log.Fatalf("unable to retrieve master key %v", err)
			}
			*tags = key
		}
	} else {
		*tags = defaultBuilderTags("gobuilder-master.key")
	}
	if *num == 0 {
		if *staging {
			*num = 5
		} else {
			*num = 50
		}
	}
	if *token == "" {
		file := filepath.Join(*tokenDir, "go-scaleway.token")
		slurp, err := ioutil.ReadFile(file)
		if err != nil {
			if os.IsNotExist(err) {
				log.Fatalf("No --token flag specified and token file %s does not exist. Googlers on the Go team can get it via http://go/golang-scaleway-token", file)
			}
			log.Fatalf("No --token specified and error reading backup token file: %v", err)
		}
		*token = strings.TrimSpace(string(slurp))
	}

	// Loop over checkServers() in daemon mode.
	if *daemonMode {
		log.Printf("scaleway instance checker daemon running.")
	}
	for {
		checkServers()
		if !*daemonMode {
			return
		}
		time.Sleep(*fixInterval)
	}
}

func checkServers() {
	timer := time.AfterFunc(5*time.Minute, func() { panic("Timeout running checkServers.") })
	defer timer.Stop()

	cl := &Client{Token: *token}
	serverList, err := cl.Servers()
	if err != nil {
		log.Fatal(err)
	}
	var names []string
	servers := map[string]*Server{}
	for _, s := range serverList {
		servers[s.Name] = s
		names = append(names, s.Name)
	}
	sort.Strings(names)
	if *listAll {
		for _, name := range names {
			s := servers[name]
			fmt.Printf("%s: %v, id=%v, state=%s, created=%v, modified=%v, image=%v\n",
				name, s.PublicIP, s.ID, s.State, s.CreationDate, s.ModificationDate, s.Image)
		}
		return
	}
	for i := 1; i <= *num; i++ {
		name := serverName(i)
		if _, ok := servers[name]; !ok {
			servers[name] = &Server{Name: name}
			names = append(names, name)
		}
	}
	sort.Strings(names)

	for name, revBuilder := range getConnectedMachines() {
		if _, ok := servers[name]; !ok {
			log.Printf("Machine connected to farmer.golang.org is unknown to scaleway: %v; ignoring", name)
			continue
		}
		servers[name].Connected = revBuilder
	}

	if *list {
		for _, name := range names {
			s := servers[name]
			status := "NOT_CONNECTED"
			if s.Connected != nil {
				status = "ok"
			}
			fmt.Printf("%s: %s, %v, id=%v, state=%s, created=%v, modified=%v, image=%v\n",
				name, status, s.PublicIP, s.ID, s.State, s.CreationDate, s.ModificationDate, s.Image)
		}
	}

	for i := 1; i <= *num; i++ {
		name := serverName(i)
		server := servers[name]

		if server.Image != nil && server.Image.ID != *image {
			log.Printf("server %s, state %q, running wrong image %s (want %s)", name, server.State, server.Image.ID, *image)
			switch server.State {
			case "running":
				log.Printf("powering off %s ...", name)
				if err := cl.PowerOff(server.ID); err != nil {
					log.Printf("PowerOff(%q (%q)): %v", server.ID, name, err)
				}
			case "stopped":
				log.Printf("deleting %s ...", name)
				if err := cl.Delete(server.ID); err != nil {
					log.Printf("Delete(%q (%q)): %v", server.ID, name, err)
				}
			}
		}

		if server.Connected != nil {
			continue
		}

		if server.State == "running" {
			if time.Time(server.ModificationDate).Before(time.Now().Add(15 * time.Minute)) {
				log.Printf("rebooting old running-but-disconnected %q server...", name)
				err := cl.serverAction(server.ID, "reboot")
				log.Printf("reboot(%q): %v", name, err)
				continue
			}
			// Started recently. Maybe still booting.
			continue
		}
		if server.State != "" {
			log.Printf("server %q in state %q; not creating", name, server.State)
			continue
		}
		tags := strings.Split(*tags, ",")
		if *staging {
			tags = append(tags, "staging")
		}
		body, err := json.Marshal(createServerRequest{
			Org:            *org,
			Name:           name,
			Image:          *image,
			CommercialType: ctype,
			Tags:           tags,
			EnableIPV6:     *ipv6,
			BootType:       "bootscript", // the "local" boot mode doesn't work on C1,
			Bootscript:     *bootscript,
		})
		if err != nil {
			log.Fatal(err)
		}
		log.Printf("sending createServerRequest: %s", body)
		// TODO: update to their new API path format that includes the zone.
		req, err := http.NewRequest("POST", scalewayAPIBase+"/servers", bytes.NewReader(body))
		if err != nil {
			log.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Auth-Token", *token)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			log.Fatal(err)
		}
		if res.StatusCode == http.StatusOK {
			log.Printf("created %v", i)
		} else {
			slurp, _ := ioutil.ReadAll(io.LimitReader(res.Body, 4<<10))
			log.Printf("creating number %v, %s: %s", i, res.Status, slurp)
		}
		res.Body.Close()
	}

	serverList, err = cl.Servers()
	if err != nil {
		log.Fatal(err)
	}
	for _, s := range serverList {
		if strings.HasSuffix(s.Name, "-prep") || strings.HasSuffix(s.Name, "-hand") {
			continue
		}
		if s.State == "stopped" {
			log.Printf("Powering on %s (%s) = %v", s.Name, s.ID, cl.PowerOn(s.ID))
		}
	}
}

type createServerRequest struct {
	Org            string   `json:"organization"`
	Name           string   `json:"name"`
	Image          string   `json:"image"`
	CommercialType string   `json:"commercial_type"`
	Tags           []string `json:"tags"`
	EnableIPV6     bool     `json:"enable_ipv6,omitempty"`
	BootType       string   `json:"boot_type,omitempty"` // local, bootscript, rescue; the default of local doesn't work on C1 machines
	Bootscript     string   `json:"bootscript,omitempty"`
}

type Client struct {
	Token string
}

// Delete deletes a server. It needs to be powered off in "stopped" state first.
//
// This is currently unused. An earlier version of this tool used it briefly before
// changing to use the reboot action. We might want this later.
func (c *Client) Delete(serverID string) error {
	req, _ := http.NewRequest("DELETE", scalewayAPIBase+"/instance/v1/zones/fr-par-1/servers/"+serverID, nil)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Auth-Token", c.Token)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusNoContent {
		slurp, _ := ioutil.ReadAll(io.LimitReader(res.Body, 1<<10))
		return fmt.Errorf("error deleting %s: %v, %s", serverID, res.Status, slurp)
	}
	return nil
}

func (c *Client) PowerOn(serverID string) error {
	return c.serverAction(serverID, "poweron")
}

func (c *Client) PowerOff(serverID string) error {
	return c.serverAction(serverID, "poweroff")
}

func (c *Client) serverAction(serverID, action string) error {
	req, _ := http.NewRequest("POST", scalewayAPIBase+"/servers/"+serverID+"/action", strings.NewReader(fmt.Sprintf(`{"action":"%s"}`, action)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Auth-Token", c.Token)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode/100 != 2 {
		return fmt.Errorf("error doing %q on %s: %v", action, serverID, res.Status)
	}
	return nil
}

func (c *Client) Servers() ([]*Server, error) {
	req, _ := http.NewRequest("GET", scalewayAPIBase+"/servers?per_page=100", nil)
	req.Header.Set("X-Auth-Token", c.Token)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("failed to get Server list: %v", res.Status)
	}
	if n, _ := strconv.Atoi(res.Header.Get("X-Total-Count")); n > 100 {
		// TODO: Get all pages, not just first one. See https://developer.scaleway.com/#header-pagination.
		return nil, fmt.Errorf("results (%d) don't fit in one page (100) and pagination isn't implemented", n)
	}
	var jres struct {
		Servers []*Server `json:"servers"`
	}
	err = json.NewDecoder(res.Body).Decode(&jres)
	return jres.Servers, err
}

type Server struct {
	ID               string         `json:"id"`
	Name             string         `json:"name"`
	PublicIP         *IP            `json:"public_ip"`
	PrivateIP        string         `json:"private_ip"`
	Tags             []string       `json:"tags"`
	State            string         `json:"state"`
	Image            *Image         `json:"image"`
	CreationDate     types.Time3339 `json:"creation_date"`
	ModificationDate types.Time3339 `json:"modification_date"`

	// Connected is non-nil if the server is connected to farmer.golang.org.
	// This does not come from the Scaleway API.
	Connected *revtype.ReverseBuilder `json:"-"`
}

type Image struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

func (im *Image) String() string {
	if im == nil {
		return "<no Image>"
	}
	return im.ID
}

type IP struct {
	ID      string `json:"id"`
	Address string `json:"address"`
}

func (ip *IP) String() string {
	if ip == nil {
		return "<no IP>"
	}
	return ip.Address
}

// defaultBuilderTags returns the default value of the "tags" flag.
// It returns a comma-separated list of builder tags (each of the form buildkey_$(BUILDER)_$(SECRETHEX)).
func defaultBuilderTags(baseKeyFile string) string {
	keyFile := filepath.Join(*tokenDir, baseKeyFile)
	slurp, err := ioutil.ReadFile(keyFile)
	if err != nil {
		log.Fatal(err)
	}
	var tags []string
	for _, builder := range []string{
		"host-linux-arm-scaleway",
	} {
		h := hmac.New(md5.New, bytes.TrimSpace(slurp))
		h.Write([]byte(builder))
		tags = append(tags, fmt.Sprintf("buildkey_%s_%x", builder, h.Sum(nil)))
	}
	return strings.Join(tags, ",")
}

func serverName(i int) string {
	if *staging {
		return fmt.Sprintf("scaleway-staging-%02d", i)
	}
	return fmt.Sprintf("scaleway-prod-%02d", i)
}

func getConnectedMachines() map[string]*revtype.ReverseBuilder {
	const reverseURL = "https://farmer.golang.org/status/reverse.json"
	res, err := http.Get(reverseURL)
	if err != nil {
		log.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		log.Fatalf("getting %s: %s", reverseURL, res.Status)
	}
	var jres revtype.ReverseBuilderStatus
	if err := json.NewDecoder(res.Body).Decode(&jres); err != nil {
		log.Fatalf("reading %s: %v", reverseURL, err)
	}
	st := jres.HostTypes["host-linux-arm-scaleway"]
	if st == nil {
		return nil
	}
	return st.Machines
}

func mustCreateSecretClient() *secret.Client {
	client, err := secret.NewClient()
	if err != nil {
		log.Fatalf("unable to create secret client %v", err)
	}
	return client
}
