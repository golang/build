# Illumos Builder

This instructions for the Illumos builder that the Go team runs on Joyent.

# Prep files from Linux

```
bradfitz@go:~/go/src$ GOOS=illumos GOARCH=amd64 BOOTSTRAP_FORMAT=mintgz ./bootstrap.bash
...
...
Writing gobootstrap-illumos-amd64-e883d000f4.tar.gz ...
-rw-r--r-- 1 bradfitz bradfitz 51647155 May 29 17:24 /home/bradfitz/gobootstrap-illumos-amd64-e883d000f4.tar.gz

bradfitz@go:~/go/src$ go install golang.org/x/build/cmd/upload
bradfitz@go:~/go/src$ upload --file=/home/bradfitz/gobootstrap-illumos-amd64-e883d000f4.tar.gz --public go-builder-data/gobootstrap-illumos-amd64-e883d000f4.tar.gz

$ cd $GOPATH/src/golang.org/x/build/cmd/buildlet
$ make buildlet.illumos-amd64
$ cd $GOPATH/src/golang.org/x/build/cmd/buildlet/stage0
$ make buildlet-stage0.illumos-amd64

```

# Create VM on Joyent

* at least 2 CPUs, at least 1 GB ram. (I used `g4-highcpu-4G` somewhat arbitrarily)

# Prep VM

```
bradfitz@go:~$ ssh -i ~/.ssh/id_rsa_golang2 root@$IP
...
# curl -O https://storage.googleapis.com/go-builder-data/gobootstrap-illumos-amd64-e883d000f4.tar.gz
# mkdir goboot
# cd goboot
# tar -zxvf ../gobootstrap-illumos-amd64-e883d000f4.tar.gz
# pkgin in gcc47
# rm /opt/local/sbin/mysqld
# rm /opt/local/sbin/httpd
# cat > /root/.gobuildkey-host-illumos-amd64-joyent
xxxx
^D
# curl -o /opt/buildlet-stage0 https://storage.googleapis.com/go-builder-data/buildlet-stage0.illumos-amd64
# chmod +x /opt/buildlet-stage0
# curl -O https://raw.githubusercontent.com/golang/build/master/env/solaris-amd64/joyent/buildlet.xml
# svccfg import buildlet.xml
```

The service should be running now. Shut down the machine and create an
image from the Joyent web console.

If you need to debug, you can check status with:

```
svcs -x buildlet.xml
```

This will also give you the location to the log file.

If you need to change an environment variable, place this inside the start
exec_method element:

```
<method_context>
    <method_environment>
       <envvar name="EXAMPLE" value="foo"/>
    </method_environment>
</method_context>
```

To debug an instance once it's running, you can ssh as:

```
$ ssh -i ~/.ssh/id_rsa_golang2 root@$IP
```

The key is at http://go/id_rsa_golang2
