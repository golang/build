// Copyright 2014 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package buildgo

import (
	"context"
	"errors"
	"fmt"
	"log"
	"path"
	"sort"
	"strings"
	"time"

	"golang.org/x/build/dashboard"
	compute "google.golang.org/api/compute/v1"
)

// MakeBasepinDisks looks at the list of all the project's VM images
// and creates (if needed) a disk for each one, named with the prefix
// "basepin-". The purpose of these "basepin" disks is to speed up GCE
// VM creations. When GCE creates a new disk for a new VM, it takes a
// fast path when creating the disk if there's another disk in the
// same zone for that image, and makes the new disk a thin
// Copy-on-Write shadow over the basepin disk. GCE also does this if
// there's been a VM created within the past N minutes of that type,
// but we want VMs to always create quickly.
func (c *Client) MakeBasepinDisks(ctx context.Context) error {
	currentImage := map[string]bool{}
	for _, hc := range dashboard.Hosts {
		if hc.VMImage != "" {
			currentImage[hc.VMImage] = true
		}
	}

	// Try to find it by name.
	svc := c.Compute()
	imList, err := svc.Images.List(c.Env.ProjectName).Do()
	if err != nil {
		return fmt.Errorf("Error listing images for %s: %v", c.Env.ProjectName, err)
	}
	if imList.NextPageToken != "" {
		return errors.New("too many images; pagination not supported")
	}

	getNeedMap := func() map[string]*compute.Image {
		need := make(map[string]*compute.Image) // keys like "https://www.googleapis.com/compute/v1/projects/symbolic-datum-552/global/images/linux-buildlet-arm"
		for _, im := range imList.Items {
			imBase := path.Base(im.SelfLink)
			if strings.Contains(im.SelfLink, "-debug") {
				continue
			}
			if !currentImage[imBase] {
				continue
			}
			need[im.SelfLink] = im
		}
		return need
	}

	for _, zone := range c.Env.VMZones {
		diskList, err := svc.Disks.List(c.Env.ProjectName, zone).Do()
		if err != nil {
			return fmt.Errorf("error listing disks in zone %v: %v", zone, err)
		}
		if diskList.NextPageToken != "" {
			return fmt.Errorf("too many disks in %v; pagination not supported (yet?)", zone)
		}

		need := getNeedMap()

		for _, d := range diskList.Items {
			if !strings.HasPrefix(d.Name, "basepin-") {
				continue
			}
			if si, ok := need[d.SourceImage]; ok && d.SourceImageId == fmt.Sprint(si.Id) {
				if c.Verbose {
					log.Printf("basepin: have %s: %s (%v)\n", d.Name, d.SourceImage, d.SourceImageId)
				}
				delete(need, d.SourceImage)
				continue
			}
			if zone != c.Env.ControlZone {
				log.Printf("basepin: deleting unnecessary disk %v in zone %v", d.Name, zone)
				op, err := svc.Disks.Delete(c.Env.ProjectName, zone, d.Name).Do()
				if err != nil {
					log.Printf("basepin: failed to delete disk %v in zone %v: %v", d.Name, zone, err)
					continue
				}
				if err := c.AwaitOp(ctx, op); err != nil {
					log.Printf("basepin: failed to delete disk %v in zone %v: %v", d.Name, zone, err)
					continue
				}
			}
		}

		var needed []string
		for imageName := range need {
			needed = append(needed, imageName)
		}
		sort.Strings(needed)
		for _, n := range needed {
			log.Printf("basepin: need %v disk in %v", n, zone)
		}
		for i, imName := range needed {
			im := need[imName]
			log.Printf("basepin: (%d/%d) creating %s ...", i+1, len(needed), im.Name)
			op, err := svc.Disks.Insert(c.Env.ProjectName, zone, &compute.Disk{
				Description:   "zone-cached basepin image of " + im.Name,
				Name:          "basepin-" + im.Name + "-" + fmt.Sprint(im.Id),
				SizeGb:        im.DiskSizeGb,
				SourceImage:   im.SelfLink,
				SourceImageId: fmt.Sprint(im.Id),
				Type:          "https://www.googleapis.com/compute/v1/projects/" + c.Env.ProjectName + "/zones/" + zone + "/diskTypes/pd-ssd",
			}).Do()
			if err != nil {
				return err
			}
			if err := c.AwaitOp(ctx, op); err != nil {
				return fmt.Errorf("basepin: failed to create: %v", err)
			}
		}
		log.Printf("basepin: created %d images in %v", len(needed), zone)
	}
	return nil
}

// AwaitOp waits for op to finish. It returns nil if the operating
// finished successfully.
func (c *Client) AwaitOp(ctx context.Context, op *compute.Operation) error {
	// TODO: clean this up with respect to status updates & logging.
	svc := c.Compute()
	opName := op.Name
	// TODO: move logging to Client c.logger. and add Client.WithLogger shallow copier.
	log.Printf("Waiting on operation %v (in %q)", opName, op.Zone)
	for {
		time.Sleep(2 * time.Second)
		op, err := svc.ZoneOperations.Get(c.Env.ProjectName, path.Base(op.Zone), opName).Do()
		if err != nil {
			return fmt.Errorf("Failed to get op %s: %v", opName, err)
		}
		switch op.Status {
		case "PENDING", "RUNNING":
			log.Printf("Waiting on operation %v", opName)
			continue
		case "DONE":
			if op.Error != nil {
				var last error
				for _, operr := range op.Error.Errors {
					log.Printf("Error: %+v", operr)
					last = fmt.Errorf("%v", operr)
				}
				return last
			}
			return nil
		default:
			return fmt.Errorf("Unknown status %q: %+v", op.Status, op)
		}
	}
}
