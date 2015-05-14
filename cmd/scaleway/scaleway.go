// Copyright 2015 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// The scaleway command creates ARM servers on Scaleway.com.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

var (
	token = flag.String("token", "", "API token")
	org   = flag.String("org", "1f34701d-668b-441b-bf08-0b13544e99de", "Organization ID (default is bradfitz@golang.org's account)")
	image = flag.String("image", "b9fcca88-fa85-4606-a2b2-3c8a7ff94fbd", "Disk image ID; default is the snapshot we made last")
	num   = flag.Int("n", 20, "Number of servers to create")
)

func main() {
	flag.Parse()
	if *token == "" {
		file := filepath.Join(os.Getenv("HOME"), "keys/go-scaleway.token")
		slurp, err := ioutil.ReadFile(file)
		if err != nil {
			log.Fatalf("No --token specified and error reading backup token file: %v", err)
		}
		*token = strings.TrimSpace(string(slurp))
	}

	cl := &Client{Token: *token}
	serverList, err := cl.Servers()
	if err != nil {
		log.Fatal(err)
	}
	servers := map[string]*Server{}
	for _, s := range serverList {
		servers[s.Name] = s
	}

	for i := 1; i <= *num; i++ {
		name := fmt.Sprintf("go-build-%d", i)
		_, ok := servers[name]
		if !ok {
			body, err := json.Marshal(createServerRequest{
				Org:   *org,
				Name:  name,
				Image: *image,
			})
			if err != nil {
				log.Fatal(err)
			}
			log.Printf("Doing req %q for token %q", body, *token)
			req, err := http.NewRequest("POST", "https://api.scaleway.com/servers", bytes.NewReader(body))
			if err != nil {
				log.Fatal(err)
			}
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("X-Auth-Token", *token)
			res, err := http.DefaultClient.Do(req)
			if err != nil {
				log.Fatal(err)
			}
			log.Printf("Create of %v: %v", i, res.Status)
			res.Body.Close()
		}
	}

	serverList, err = cl.Servers()
	if err != nil {
		log.Fatal(err)
	}
	for _, s := range serverList {
		if s.State == "stopped" {
			log.Printf("Powering on %s = %v", s.ID, cl.PowerOn(s.ID))
		}
	}
}

type createServerRequest struct {
	Org   string `json:"organization"`
	Name  string `json:"name"`
	Image string `json:"image"`
}

type Client struct {
	Token string
}

func (c *Client) PowerOn(serverID string) error {
	return c.serverAction(serverID, "poweron")
}

func (c *Client) serverAction(serverID, action string) error {
	req, _ := http.NewRequest("POST", "https://api.scaleway.com/servers/"+serverID+"/action", strings.NewReader(fmt.Sprintf(`{"action":"%s"}`, action)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Auth-Token", c.Token)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode/100 != 2 {
		return fmt.Errorf("Error doing %q on %s: %v", action, serverID, res.Status)
	}
	return nil
}

func (c *Client) Servers() ([]*Server, error) {
	req, _ := http.NewRequest("GET", "https://api.scaleway.com/servers", nil)
	req.Header.Set("X-Auth-Token", c.Token)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return nil, fmt.Errorf("Failed to get Server list: %v", res.Status)
	}
	var jres struct {
		Servers []*Server `json:"servers"`
	}
	err = json.NewDecoder(res.Body).Decode(&jres)
	return jres.Servers, err
}

type Server struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	PublicIP  *IP      `json:"public_ip"`
	PrivateIP string   `json:"private_ip"`
	Tags      []string `json:"tags"`
	State     string   `json:"state"`
	Image     *Image   `json:"image"`
}

type Image struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type IP struct {
	ID      string `json:"id"`
	Address string `json:"address"`
}
