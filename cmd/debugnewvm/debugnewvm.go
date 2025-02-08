// Copyright 2017 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// The debugnewvm command creates and destroys a VM-based buildlet
// with lots of logging for debugging. Nothing depends on this.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"path"
	"regexp"
	"strings"
	"time"

	"cloud.google.com/go/compute/metadata"
	"golang.org/x/build/buildenv"
	"golang.org/x/build/buildlet"
	"golang.org/x/build/dashboard"
	"golang.org/x/build/internal/buildgo"
	"golang.org/x/build/internal/cloud"
	"golang.org/x/build/internal/secret"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	compute "google.golang.org/api/compute/v1"
)

var (
	hostType      = flag.String("host", "", "host type to create")
	zone          = flag.String("zone", "", "if non-empty, force a certain GCP zone")
	overrideImage = flag.String("override-image", "", "if non-empty, an alternate GCE VM image or container image to use, depending on the host type")
	serial        = flag.Bool("serial", true, "watch serial. Supported for GCE VMs")
	pauseAfterUp  = flag.Duration("pause-after-up", 0, "pause for this duration before buildlet is destroyed")
	sleepSec      = flag.Int("sleep-test-secs", 0, "number of seconds to sleep when buildlet comes up, to test time source; OpenBSD only for now")

	runBuild = flag.String("run-build", "", "optional builder name to run all.bash or make.bash for")
	makeOnly = flag.Bool("make-only", false, "if a --run-build builder name is given, this controls whether make.bash or all.bash is run")
	buildRev = flag.String("rev", "master", "if --run-build is specified, the git hash or branch name to build")

	useIAPTunnel = flag.Bool("use-iap-tunnel", true, "use an IAP tunnel to connect to GCE builders")

	awsKeyID     = flag.String("aws-key-id", "", "if the builder runs on aws then key id is required. If executed on GCE, it will be retrieved from secrets.")
	awsAccessKey = flag.String("aws-access-key", "", "if the builder runs on aws then the access key is required. If executed on GCE, it will be retrieved from secrets.")
	awsRegion    = flag.String("aws-region", "", "if non-empty and the requested builder is an EC2 instance, force an EC2 region.")
)

var (
	computeSvc *compute.Service
	env        *buildenv.Environment
)

func main() {
	buildenv.RegisterFlags()
	flag.Parse()

	var bconf *dashboard.BuildConfig
	if *runBuild != "" {
		var ok bool
		bconf, ok = dashboard.Builders[*runBuild]
		if !ok {
			log.Fatalf("unknown builder %q", *runBuild)
		}
		if *hostType == "" {
			*hostType = bconf.HostType
		}
	}

	if *hostType == "" {
		log.Fatalf("missing --host (or --run-build)")
	}
	if *sleepSec != 0 && !strings.Contains(*hostType, "openbsd") {
		log.Fatalf("The --sleep-test-secs is currently only supported for openbsd hosts.")
	}

	hconf, ok := dashboard.Hosts[*hostType]
	if !ok {
		log.Fatalf("unknown host type %q", *hostType)
	}
	if !hconf.IsVM() && !hconf.IsContainer() {
		log.Fatalf("host type %q is type %q; want a VM or container host type", *hostType, hconf.PoolName())
	}
	if hconf.IsEC2 && (*awsKeyID == "" || *awsAccessKey == "") {
		if !metadata.OnGCE() {
			log.Fatal("missing -aws-key-id and -aws-access-key params are required for builders on AWS")
		}
		var err error
		*awsKeyID, *awsAccessKey, err = awsCredentialsFromSecrets()
		if err != nil {
			log.Fatalf("unable to retrieve AWS credentials: %s", err)
		}
	}
	if img := *overrideImage; img != "" {
		if hconf.IsContainer() {
			hconf.ContainerImage = img
		} else {
			hconf.VMImage = img
		}
	}
	vmImageSummary := fmt.Sprintf("%q", hconf.VMImage)
	if hconf.IsContainer() {
		containerHost := hconf.ContainerVMImage()
		if containerHost == "" {
			containerHost = "default container host"
		}
		vmImageSummary = fmt.Sprintf("%s, running container %q", containerHost, hconf.ContainerImage)
	}

	env = buildenv.FromFlags()
	ctx := context.Background()
	name := fmt.Sprintf("debug-temp-%d-%s", time.Now().Unix(), os.Getenv("USER"))

	log.Printf("Creating %s (with VM image %s)", name, vmImageSummary)
	var bc buildlet.Client
	if hconf.IsEC2 {
		region := env.AWSRegion
		if *awsRegion != "" {
			region = *awsRegion
		}
		awsC, err := cloud.NewAWSClient(region, *awsKeyID, *awsAccessKey)
		if err != nil {
			log.Fatalf("unable to create aws cloud client: %s", err)
		}
		ec2C := buildlet.NewEC2Client(awsC)
		if err != nil {
			log.Fatalf("unable to create ec2 client: %v", err)
		}
		bc, err = ec2Buildlet(context.Background(), ec2C, hconf, env, name, *hostType, *zone)
		if err != nil {
			log.Fatalf("Start EC2 VM: %v", err)
		}
	} else {
		buildenv.CheckUserCredentials()
		creds, err := env.Credentials(ctx)
		if err != nil {
			log.Fatal(err)
		}
		computeSvc, _ = compute.New(oauth2.NewClient(ctx, creds.TokenSource))
		bc, err = gceBuildlet(creds, env, name, *hostType, *zone)
		if err != nil {
			log.Fatalf("Start GCE VM: %v", err)
		}
	}
	dir, err := bc.WorkDir(ctx)
	log.Printf("WorkDir: %v, %v", dir, err)

	if *sleepSec > 0 {
		bc.Exec(ctx, "sysctl", buildlet.ExecOpts{
			Output:      os.Stdout,
			SystemLevel: true,
			Args:        []string{"kern.timecounter.hardware"},
		})
		bc.Exec(ctx, "bash", buildlet.ExecOpts{
			Output:      os.Stdout,
			SystemLevel: true,
			Args:        []string{"-c", "rdate -p -v time.nist.gov; sleep " + fmt.Sprint(*sleepSec) + "; rdate -p -v time.nist.gov"},
		})
	}

	var buildFailed bool
	if *runBuild != "" {
		// Push GOROOT_BOOTSTRAP, if needed.
		if u := bconf.GoBootstrapURL(env); u != "" {
			log.Printf("Pushing 'go1.4' Go bootstrap dir from %s...", u)
			const bootstrapDir = "go1.4" // might be newer; name is the default
			if err := bc.PutTarFromURL(ctx, u, bootstrapDir); err != nil {
				bc.Close()
				log.Fatalf("Putting Go bootstrap: %v", err)
			}
		}

		// Push Go code
		log.Printf("Pushing 'go' dir...")
		goTarGz := "https://go.googlesource.com/go/+archive/" + *buildRev + ".tar.gz"
		if err := bc.PutTarFromURL(ctx, goTarGz, "go"); err != nil {
			bc.Close()
			log.Fatalf("Putting go code: %v", err)
		}

		// Push a synthetic VERSION file to prevent git usage:
		if err := bc.PutTar(ctx, buildgo.VersionTgz(*buildRev), "go"); err != nil {
			bc.Close()
			log.Fatalf("Putting VERSION file: %v", err)
		}

		script := bconf.AllScript()
		if *makeOnly {
			script = bconf.MakeScript()
		}
		t0 := time.Now()
		log.Printf("Running %s ...", script)
		remoteErr, err := bc.Exec(ctx, path.Join("go", script), buildlet.ExecOpts{
			Output:   os.Stdout,
			ExtraEnv: bconf.Env(),
			Debug:    true,
			Args:     bconf.AllScriptArgs(),
		})
		if err != nil {
			log.Fatalf("error trying to run %s: %v", script, err)
		}
		if remoteErr != nil {
			log.Printf("remote failure running %s: %v", script, remoteErr)
			buildFailed = true
		} else {
			log.Printf("ran %s in %v", script, time.Since(t0).Round(time.Second))
		}
	}

	if *pauseAfterUp != 0 {
		log.Printf("Sleeping for %v before shutting down...", *pauseAfterUp)
		time.Sleep(*pauseAfterUp)
	}
	if err := bc.Close(); err != nil {
		log.Fatalf("Close: %v", err)
	}
	log.Printf("done.")
	time.Sleep(2 * time.Second) // wait for serial logging to catch up

	if buildFailed {
		os.Exit(1)
	}
}

// watchSerial streams the named VM's serial port to log.Printf. It's roughly:
//
//	gcloud compute connect-to-serial-port --zone=xxx $NAME
//
// but in Go and works. For some reason, gcloud doesn't work as a
// child process and has weird errors.
// TODO(golang.org/issue/39485) - investigate if this is possible for EC2 instances
func watchSerial(zone, name string) {
	start := int64(0)
	indent := strings.Repeat(" ", len("2017/07/25 06:37:14 SERIAL: "))
	for {
		sout, err := computeSvc.Instances.GetSerialPortOutput(env.ProjectName, zone, name).Start(start).Do()
		if err != nil {
			log.Printf("serial output error: %v", err)
			return
		}
		moved := sout.Next != start
		start = sout.Next
		contents := strings.Replace(strings.TrimSpace(sout.Contents), "\r\n", "\r\n"+indent, -1)
		if contents != "" {
			log.Printf("SERIAL: %s", contents)
		}
		if !moved {
			time.Sleep(1 * time.Second)
		}
	}
}

// awsCredentialsFromSecrets retrieves AWS credentials from the secret management service.
// This function returns the key ID and the access key.
func awsCredentialsFromSecrets() (string, string, error) {
	c, err := secret.NewClient()
	if err != nil {
		return "", "", fmt.Errorf("unable to create secret client: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	keyID, err := c.Retrieve(ctx, secret.NameAWSKeyID)
	if err != nil {
		return "", "", fmt.Errorf("unable to retrieve key ID: %w", err)
	}
	accessKey, err := c.Retrieve(ctx, secret.NameAWSAccessKey)
	if err != nil {
		return "", "", fmt.Errorf("unable to retrieve access key: %w", err)
	}
	return keyID, accessKey, nil
}

func gceBuildlet(creds *google.Credentials, env *buildenv.Environment, name, hostType, zone string) (buildlet.Client, error) {
	return buildlet.StartNewVM(creds, env, name, hostType, buildlet.VMOpts{
		Zone:                zone,
		OnInstanceRequested: func() { log.Printf("instance requested") },
		OnInstanceCreated: func() {
			log.Printf("instance created")
		},
		OnGotInstanceInfo: func(inst *compute.Instance) {
			zone := inst.Zone
			m := regexp.MustCompile(`/projects/([^/]+)/zones/([^/]+)`).FindStringSubmatch(inst.Zone)
			if m != nil {
				zone = m[2]
			}
			log.Printf("got instance info; running in %v (%v)", inst.Zone, zone)
			if *serial {
				go watchSerial(zone, name)
			}
		},
		OnBeginBuildletProbe: func(buildletURL string) {
			log.Printf("About to hit %s to see if buildlet is up yet...", buildletURL)
		},
		OnEndBuildletProbe: func(res *http.Response, err error) {
			if err != nil {
				log.Printf("client buildlet probe error: %v", err)
				return
			}
			log.Printf("buildlet probe: %s", res.Status)
		},
		UseIAPTunnel: *useIAPTunnel,
	})
}

func ec2Buildlet(ctx context.Context, ec2Client *buildlet.EC2Client, hconf *dashboard.HostConfig, env *buildenv.Environment, name, hostType, zone string) (buildlet.Client, error) {
	kp, err := buildlet.NewKeyPair()
	if err != nil {
		log.Fatalf("key pair failed: %v", err)
	}
	return ec2Client.StartNewVM(ctx, env, hconf, name, hostType, &buildlet.VMOpts{
		TLS:                 kp,
		Zone:                zone,
		OnInstanceRequested: func() { log.Printf("instance requested") },
		OnInstanceCreated: func() {
			log.Printf("instance created")
		},
		OnGotEC2InstanceInfo: func(inst *cloud.Instance) {
			log.Printf("got instance info: running in %v", inst.Zone)
		},
		OnBeginBuildletProbe: func(buildletURL string) {
			log.Printf("About to hit %s to see if buildlet is up yet...", buildletURL)
		},
		OnEndBuildletProbe: func(res *http.Response, err error) {
			if err != nil {
				log.Printf("client buildlet probe error: %v", err)
				return
			}
			log.Printf("buildlet probe: %s", res.Status)
		},
	})
}
