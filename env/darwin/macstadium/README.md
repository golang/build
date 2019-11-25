# Overview

Go's Mac builders run at Mac hosting provider,
[MacStadium](https://macstadium.com). We have a VMware cluster with 10
physical Mac Minis on which we run two VMs each.

In addition to 20 Mac VMs, we also run one 1 Linux VM that's our
bastion host & runs various services.

## Bastion Host

The bastion host is **macstadiumd.golang.org** and can be accessed
via:

    $ ssh -i ~/keys/id_ed25519_golang1 gopher@macstadiumd.golang.org

(Where `id_ed255519_golang1` is available from http://go/go-builders-ssh)

It also runs:

* a DHCP server for the 20 Mac VMs to get IP addresses from (systemd
  unit `isc-dhcp-server.service`, so watch with `journalctl -f -u
  isc-dhcp-server.service`)

* the [**makemac** daemon](../../../cmd/makemac/) daemon (systemd
  unit `makemac.service`, so watch with `journalctl -f -u makemac`).
  This monitors the build coordinator (farmer.golang.org) as well as
  the VMware cluster (via the [`govc` CLI
  tool](https://github.com/vmware/govmomi/tree/master/govc)) and makes
  and destroys Mac VMs as needed. It also serves plaintext HTTP status
  at http://macstadiumd.golang.org:8713 which used to be accessible in
  a browser, but recent HSTS configuration on `*.golang.org` means you
  need to use curl now. But that's a good way to see what it's doing
  remotely, without authentication.

* [WireGuard](https://www.wireguard.com/) listening on port 51820,
  because OpenVPN + Mac's built-in VPN client is painful and buggy.
  See config in `/etc/wireguard/wg0.conf`. This doesn't yet come up on
  boot. It's a recent addition.

## OpenVPN

The method of last resort to access the cluster, which works even if
the bastion host VM is down, is to VPN to our Cisco something gateway.

TODO: put this info somewhere. It's currently circulated in email
between Brad, Russ, and Dmitri in an email with some subject "macOS VM
info".

## VMware web UI

Once you've connected to either OpenVPN or WireGuard, you can hit the
VMware web UI at:

   https://10.88.203.9/ui/

(Alternatively, `ssh -D` to the bastion host to make a SOCKS tunnel
and configure your browser to send proxy through that SOCKS tunnel.)

## Adding a New Image

When a new version of macOS is released:

* Ensure that the version of vSphere deployed on MacStadium supports the
  new version of macOS. If it doesn't, either request that MacStadium
  upgrade the cluster or seek guidance from them about the upgrade path.

* Clone the latest macOS version on vSphere and upgrade that version
  to the desired macOS version as per the [instructions](vmware-notes.md).

* If a completely new image is required, follow the [images setup notes](image-setup-notes.txt)
  in order to add a new image.

## Debugging

Common techniques to debug:

* Can you get to the bastion host?

* What does `journalctl -f -u makemac` say? Is it error looping?

* Look at https://10.88.203.9/ui/ and see if VMware is unhappy about
  things. Did hosts die? Did storage disappear?

* Need to hard reboot machines? Eventually we'll fix
  https://github.com/golang/go/issues/32033 but for now you can use
  https://portal.macstadium.com/subscriptions and power cycle
  machines that VMware reports dead (or that you see missing from
  https://farmer.golang.org's reverse pool info).

* Worst case, file a ticket: https://portal.macstadium.com/tickets
