# See README for an overview of how to use this Makefile.

# Database of ISO names and MD5s. Variable name suffix is short version (622 for 6.2.2, see V below)
ISO_600=dfly-x86_64-6.0.0_REL.iso
MD5_600=252fc700803dbadce1de928432373bbb

ISO_622=dfly-x86_64-6.2.2_REL.iso
MD5_622=b391a29c8e9c1cc33f2441ff1f0d98a0

ISO_640=dfly-x86_64-6.4.0_REL.iso
MD5_640=5dbf894d9120664a675030c475f25040

# Default version when invoking make. Can override on command line: make V=600
V=640

ISO=$(ISO_$V)
MD5=$(MD5_$V)

default: disk-$V.tar.gz

clean:
	rm -f *.iso *.iso.bz2 phase*.iso isotmp disk-*.phase* disk-*.tar.gz disk.raw disk.tmp

# Download image from DragonflyBSD page.
$(ISO):
	curl -o $@.bz2 https://mirror-master.dragonflybsd.org/iso-images/$@.bz2
	echo "$(MD5)  $@.bz2" | md5sum -c -
	bunzip2 $@.bz2

# If Dragonfly boots with a CD containing pfi.conf that says pfi_script=name.sh,
# then it runs name.sh at the end of the standard startup process.
# We use this to run a script inside a QEMU image.
# phase1.iso is a CD containing phase1.sh, and so on.
%.iso: %.sh
	rm -rf isotmp
	mkdir isotmp
	cp $^ isotmp
	echo pfi_script=$*.sh >>isotmp/pfi.conf
	chmod +x isotmp/$*.sh
	rm -f $@
	genisoimage -r -o $@ isotmp/
	rm -rf isotmp

# Disk image creation is broken into phases so that later phases
# can be modified and rerun without repeating the earlier phases,
# which can take a long time.

# Phase 1 sets up a basic DragonflyBSD install on the disk.
# The installer is CD0, the PFI script is CD1, and the installer is also CD2.
# CD2 is used by phase1.sh to get a clean FS image with nothing mounted on it.
disk-$V.phase1: $(ISO) phase1.iso
	qemu-img create -f qcow2 $@ 16G
	(sleep 1; echo 9; echo set console=comconsole; echo boot) | \
	qemu-system-x86_64 \
		-display none -serial stdio \
		-m 4G -net nic,model=virtio -net user -device virtio-scsi-pci,id=scsi0 \
		-drive file=$@,if=none,format=qcow2,cache=none,id=myscsi -device scsi-hd,drive=myscsi,bus=scsi0.0 \
		-drive file=$(ISO),media=cdrom \
		-drive file=phase1.iso,media=cdrom \
		-drive file=$(ISO),media=cdrom

# Phase 2 updates the pkg database and installing curl, git, and so on.
disk-$V.phase2: disk-$V.phase1 phase2.iso
	cp $< $@
	qemu-system-x86_64 \
		-display none -serial stdio \
		-m 4G -net nic,model=virtio -net user -device virtio-scsi-pci,id=scsi0 \
		-drive file=$@,if=none,format=qcow2,cache=none,id=myscsi -device scsi-hd,drive=myscsi,bus=scsi0.0 \
		-drive file=phase2.iso,media=cdrom

# Phase 3 customizes the image to run the buildlet.
phase3.iso: buildlet
disk-$V.phase3: disk-$V.phase2 phase3.iso
	cp $< $@
	qemu-system-x86_64 \
		-display none -serial stdio \
		-m 4G -net nic,model=virtio -net user -device virtio-scsi-pci,id=scsi0 \
		-drive file=$@,if=none,format=qcow2,cache=none,id=myscsi -device scsi-hd,drive=myscsi,bus=scsi0.0 \
		-drive file=phase3.iso,media=cdrom

# For testing and exploration
run: disk-$V.phase3
	cp $< disk.tmp
	qemu-system-x86_64 \
		-display none -serial stdio \
		-m 4G -net nic,model=virtio -net user -device virtio-scsi-pci,id=scsi0 \
		-drive file=disk.tmp,if=none,format=qcow2,cache=none,id=myscsi -device scsi-hd,drive=myscsi,bus=scsi0.0

# Convert QEMU image back to raw for Google Cloud.
disk-$V.tar.gz: disk-$V.phase3
	rm -f disk.raw
	qemu-img convert -f qcow2 -O raw -t none -T none $< disk.raw
	tar -Sczf $@ disk.raw
	rm -f disk.raw

# Upload and create prod disk image.
upload-prod: disk-$V.tar.gz
	@echo "Run 'gcloud auth application-default login' if you get credential errors."
	go run ../../cmd/upload --verbose --file=$< --public go-builder-data/dragonfly-amd64-$V.tar.gz
	gcloud compute --project symbolic-datum-552 images create dragonfly-amd64-$V --source-uri gs://go-builder-data/dragonfly-amd64-$V.tar.gz

# Delete prod disk image.
delete-prod:
	gcloud compute --project symbolic-datum-552 images delete dragonfly-amd64-$V

# Upload and create staging disk image.
upload-staging: disk-$V.tar.gz
	go run ../../cmd/upload --verbose --file=$< --public dev-go-builder-data/dragonfly-amd64-$V.tar.gz
	gcloud compute --project go-dashboard-dev images create dragonfly-amd64-$V --source-uri gs://dev-go-builder-data/dragonfly-amd64-$V.tar.gz

# Delete staging disk image.
delete-staging:
	gcloud compute --project go-dashboard-dev images delete dragonfly-amd64-$V
