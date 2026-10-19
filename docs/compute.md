# Compute

## What the compute area does

The compute area runs virtual machines for projects.
A user creates a VirtualMachine, a Volume, a VolumeSnapshot or an SSHKey in a project namespace.
The operator turns each object into KubeVirt, CDI and CSI objects.
The operator writes the result to the `status` field of the object.
The proxy gives the VM actions and the consoles.

The project namespace must have the labels `bedrock.cloudyfolks.io/tenant` and `bedrock.cloudyfolks.io/project`.
Without these labels the operator creates nothing.
The condition `Ready` shows the reason `NotInProject`.

## Kinds

### VirtualMachine

| Field | Meaning |
|---|---|
| `instanceType` | Name of a cluster instance type, for example `u1.medium`. |
| `preference` | Name of a cluster preference. The image preference is the default. |
| `image` | Name and scope (`cluster` or `project`) of the root disk image. |
| `rootDisk` | `sizeGi` and an optional `storageClass`. |
| `disks` | List of Volumes with an optional bus and an optional `serial`. |
| `cdroms` | List of ISO image names. |
| `networks` | List of Subnets with security groups, an optional MAC address and an optional fixed `address`. |
| `cloudInit` | `userData`, `networkData` or a `secretRef`. |
| `sshKeys` | Names of SSHKey objects. |
| `running` | The wanted run state. The default is `true`. |
| `evictionStrategy` | `LiveMigrate`, `LiveMigrateIfPossible` or `None`. |
| `hostDevices`, `firmware`, `cpu`, `scheduling` | Optional hardware and placement settings. |

All references are names in the namespace of the VM.
A reference must not hold `/`, `,`, `{`, a space or an upper-case letter.
The API server refuses such a reference.

### Volume

A Volume is a disk with its own life.
The source is blank, an image, a VolumeSnapshot or another Volume.
The size can grow. The size cannot shrink.
The storage class cannot change.

### VolumeSnapshot

A VolumeSnapshot is a point-in-time copy of one Volume.
The optional `retention` is a time to live.
The operator deletes the snapshot when the retention ends.
Without a retention the snapshot stays.

### SSHKey

An SSHKey holds one public key on one line.
The operator checks the key and sets the condition `Ready`.

## Objects the operator creates

Each object has the same name as its owner plus a fixed suffix.
Each object carries the labels `kind`, `name`, `tenant` and `project` of the group `bedrock.cloudyfolks.io`.
Each object has the Bedrock object as owner, so a delete removes it.

| Object | Name | Purpose |
|---|---|---|
| KubeVirt `VirtualMachine` | `<vm>` | The running machine. |
| DataVolume | `<vm>-root` | The root disk. |
| DataVolume | `<vm>-cdrom-<index>` | One claim for each ISO. |
| Secret | `<vm>-cloudinit` | The cloud-init data. |
| DataVolume | `<volume>` | The disk of a Volume. |
| CSI VolumeSnapshot | `<snapshot>` | The copy of a Volume. |

The operator creates a DataVolume once and never changes its spec.
CDI refuses such changes.
A DataVolume that is created again over a kept claim adopts the claim.

## Networks

A VM has one network interface for each Subnet in `networks`.
The first interface is `eth0` and has the default route.
The guest has no interface on the cluster network.
When `networks` is empty, the VM uses the Subnet `default` of its project.
Each Subnet can be attached once.

The operator starts a VM only when each Subnet and each security group has the condition `Ready`.
A VM never starts without its firewall.
A VM with no security group uses the default group `default-<namespace>` of its project.
The VM waits until this group exists.
Port security is on for each interface.

A change of the security groups reaches a running VM at once.
A change of the Subnets applies at the next restart.

### Fixed address

An entry of `networks` can have an `address`, an IPv4 address.
The VM keeps this address in the Subnet, also after a restart.
Without an `address`, Fabric chooses the address.
The `address` cannot change after you create the VM.
You cannot add it to an entry later, and you cannot remove it.
To use another address, change it in two updates: first remove the entry, then add it again with the new address.
The new address applies when the VM starts again.
A running VM keeps its address until then.

The operator checks the address before it starts the VM.
The check fails in these cases:

- The address is not in the CIDR of the Subnet.
- The address is the gateway, the network address, the broadcast address or an address in `excludeIPs`.
- Another NIC holds the address.
  This is the pinned address of an older VM, or an address that Fabric gave to another VM or pod of the Subnet.

When a check fails, the VM does not start.
The conditions `Ready` and `NetworkReady` show the reason.
After you remove the cause, the VM starts.
The operator checks `AddressInUse` again every 30 seconds.
Fabric can refuse the address after the VM has started, for example when two pods ask for it at the same time.
Then the pod has no address, and the VM shows `AddressInUse` after at most 30 seconds.

## Cloud-init and SSH keys

The operator always puts cloud-init data in the Secret `<vm>-cloudinit`.
With `secretRef` the operator copies the keys `userdata` and `networkdata` of your Secret.
`secretRef` cannot be used with `userData` or `networkData`.
Cloud-init runs once for each instance.
A later change does not reach a guest that has started.

Allowed key types are `ssh-ed25519`, `ecdsa-sha2-nistp256`, `ecdsa-sha2-nistp384`, `ecdsa-sha2-nistp521`, `sk-ssh-ed25519@openssh.com`, `sk-ecdsa-sha2-nistp256@openssh.com` and `ssh-rsa` with at least 2048 bits.
A key with options or a certificate is refused.
The operator keeps the key type and the key only.
The operator drops the comment.

The operator adds the keys at the end of a `#cloud-config` document.
The operator does not change the text of your document.
The keys apply to the default user of the image.
User data that is a script cannot be used together with `sshKeys`.
User data that sets `ssh_authorized_keys` cannot be used together with `sshKeys`.
User data that is not a block mapping of one document cannot be used together with `sshKeys`.
The VM then has the reason `InvalidUserData`.

## Disks

The root disk can grow. It cannot shrink.
The size of the root disk must be at least the size of the image.
When you increase `rootDisk.sizeGi`, the operator grows the claim.

A CD-ROM is cloned from an ISO image into a claim of the VM.
The operator looks for a project image first and then for a cluster image.
The image must have the type `iso`.
The CD-ROM is read-only.

A Volume can be used by one VM only.
The VM that is older wins.
The other VM has the reason `VolumeInUse`.
You can add and remove Volumes while the VM runs.
The disk bus is the bus of the VM entry or else the bus of the Volume.

A delete of a Volume waits until no VM lists the Volume.
In this time the Volume has the reason `InUse`.
You can expand a Volume when the storage class allows it.
Without this permission the Volume has the reason `ExpansionNotSupported`.

## Snapshots

A VolumeSnapshot uses the snapshot class of the storage driver of the Volume.
A Volume on a class without a snapshot class has the reason `NoSnapshotClass`.

A VM snapshot is a snapshot of the whole VM.
Use the subresource `snapshot` to create one.
The default retention is 168 hours. The maximum is 720 hours.
The operator deletes the VM snapshot when the retention ends.
The area has no restore of a VM snapshot. Use backups to restore a VM.

## Actions and consoles

The proxy checks the right of the caller before it runs an action.
The path is `/apis/bedrock.cloudyfolks.io/v1alpha1/namespaces/<namespace>/virtualmachines/<name>/<subresource>`.

| Subresource | Method | Needed right | Result |
|---|---|---|---|
| `start` | POST | `update` | 202. The VM starts. |
| `stop` | POST | `update` | 202. The VM stops. |
| `restart` | POST | `update` | 202. The VM must be Running, or the answer is 409. |
| `migrate` | POST | `update` for cluster admins | 202 with the name of the migration. |
| `snapshot` | POST | `update` | 201 with the name and the end of the retention. |
| `snapshot` | GET | `get` | 200 with the list of snapshots. |
| `console/vnc` | GET with WebSocket | `get` | 101. A byte stream to the VNC console. |
| `console/serial` | GET with WebSocket | `get` | 101. A byte stream to the serial console. |

Project owners and editors can use all subresources except `migrate`.
Project viewers cannot use a subresource.
Only cluster admins can use `migrate`.

A console needs a Running or Migrating VM.
The request must be a same-origin WebSocket request.
The proxy removes the credentials of the caller before it calls KubeVirt.
The VM status shows the console paths in `status.console`.

## Live migration status

`status.migration` shows the newest KubeVirt migration of the VM.
It has the fields `name`, `phase`, `startedAt` and `message`.
The `phase` is the phase of the KubeVirt migration, for example `Running`, `Succeeded` or `Failed`.
The `message` holds the failure reason of a failed migration.
The `startedAt` is the creation time of the migration.
The operator reads only migrations in the namespace of the VM that name the VM.
The status is empty when the VM has no migration.

## Guest identity

A guest reads its identity from the virtual hardware.
Windows activation depends on it.

The Settings `virt.smbios.manufacturer`, `virt.smbios.product`, `virt.smbios.family`, `virt.smbios.version` and `virt.smbios.sku` set the SMBIOS values of all guests.
An empty Setting keeps the KubeVirt value.
A change of one of these Settings does not restart a running VM.
The new value applies at the next start of each VM.

`firmware.uuid` and `firmware.serial` set the UUID and the serial number of one VM.
You cannot change them, add them or remove them after the VM exists.
Without them KubeVirt derives the UUID from the VM name.

`disks[].serial` sets the serial number of one disk.
Without it the serial is a fixed value that the operator derives from the Volume name.
A serial has at most 36 characters.
It can hold letters, digits, dot, underscore and dash.
A change applies at the next start of the VM.

## Settings

| Setting | Effect |
|---|---|
| `virt.smbios.manufacturer`, `.product`, `.family`, `.version`, `.sku` | The SMBIOS values of all guests. The default is empty. See Guest identity. |
| `overcommit.cpu` | The number of vCPUs for each physical thread. The default is 4. |
| `overcommit.memory` | The memory overcommit ratio. The minimum is 0.1. The default is 1.0. |
| `migration.parallel` | The number of live migrations at the same time in the cluster. |
| `migration.parallel-outbound` | The number of outbound live migrations at the same time on one node. The default is 4. |
| `migration.bandwidth` | The bandwidth limit of one live migration, in bytes per second. The default is `1Gi`. |
| `migration.completion-timeout-per-gib` | The seconds for each GiB of guest memory before KubeVirt aborts a live migration. The default is 800. |
| `migration.progress-timeout` | The seconds without progress before KubeVirt aborts a live migration. The default is 150. |
| `migration.compression` | Empty or `zstd`. The default is empty. |

The migration Settings change the KubeVirt configuration.
A running VM is not restarted and not migrated by this change.

With `migration.compression` set to `zstd`, the operator creates the KubeVirt `MigrationPolicy` `bedrock-compression`.
The policy matches all VMs and compresses the migration traffic with zstd.
The compression uses CPU on the source node and is an experimental KubeVirt feature.
When you set the Setting to empty, the operator deletes the policy.

A wrong value stops the render of the KubeVirt object.
The live object does not change.
The Setting `migration.network` is not used yet.
Migrations use the pod network.

## Instance types and preferences

KubeVirt installs the common instance types and preferences.
The operator keeps this catalog on.
The role `bedrock-instancetype-reader` lets all signed-in users read the catalog.
The role does not give any other right.

## Limits

- A VM with `cpu.dedicated` needs the static CPU manager policy of the kubelet. Without it the VM has the phase `Failed`.
- `hostDevices` needs devices that the cluster permits.
- `firmware.secureBoot` needs `firmware.uefi`.
- A VM name has at most 52 characters.
- A VM has at most 8 networks, 24 disks, 4 CD-ROMs and 16 SSH keys.
- The operator never sets `nodeName` on a pod. Use `scheduling` for placement.

## Troubleshooting

Run `kubectl describe virtualmachine <name>` and read the condition `Ready`.

| Reason | Cause | Action |
|---|---|---|
| `NotInProject` | The namespace has no project labels. | Create the object in a project namespace. |
| `InvalidSpec` | A field is wrong. | Read the message and correct the field. |
| `InvalidUserData` | The user data cannot hold the SSH keys. | Use a `#cloud-config` document. |
| `ImageNotFound` | The image does not exist. | Check the name and the scope. |
| `ImageNotReady` | The image import is not complete. | Wait or check the image status. |
| `ImageWrongType` | The image type does not fit. | Use a disk image for the root disk and an ISO for a CD-ROM. |
| `RootDiskTooSmall` | The root disk is smaller than the image. | Increase `rootDisk.sizeGi`. |
| `InstanceTypeNotFound` | The instance type does not exist. | List the cluster instance types. |
| `PreferenceNotFound` | The preference does not exist. | List the cluster preferences. |
| `SubnetNotReady` | A Subnet is missing or not ready. | Check the Subnet condition. |
| `AddressOutsideSubnet` | The `address` of a network is not in the CIDR of its Subnet. | Use an address in the CIDR. |
| `AddressReserved` | The `address` of a network is the gateway, the network address, the broadcast address or an excluded address. | Use another address. |
| `AddressInUse` | Another VM pins the `address`, or Fabric gave it to another NIC of the Subnet. | Use another address, or free the address. The operator checks again in 30 seconds. |
| `SecurityGroupNotReady` | A security group is missing or not ready. | Check the security group condition. |
| `SSHKeyNotReady` | An SSHKey is missing or not valid. | Check the SSHKey condition. |
| `VolumeNotFound` | A Volume does not exist. | Create the Volume. |
| `VolumeInUse` | Another VM uses the Volume. | Remove the Volume from one VM. |
| `VolumeNotUsable` | The Volume is not ready, or its claim belongs to another object. | Wait for the Volume condition `Ready`, or rename the Volume. |
| `RestoreInProgress` | A restore holds the object. | Wait until the restore ends. |
| `ApplyFailed` | The cluster refused an object. | Ask an administrator to read the operator log. The operator tries again in 30 seconds. |
| `NameConflict` | An object of another owner has a name that the operator needs. | Rename your Volume, Secret or snapshot. |
| `WaitingForDefaultSecurityGroup` | The default security group of the project does not exist yet. | Wait. The operator checks again in 30 seconds. |
| `SourceNotReady` | The source of a Volume is not ready. | Check the image, the snapshot or the source Volume. |
| `ShrinkRefused` | The size is smaller than the claim. | Use a size that is not smaller. |
| `InUse` | A VM lists the Volume that you delete. | Remove the Volume from the VM. |
| `VolumeNotFound` on a snapshot | The Volume or its claim does not exist. | Create the Volume first. |
| `NoSnapshotClass` | No snapshot class serves the Volume. | Use a storage class with a snapshot class. |
