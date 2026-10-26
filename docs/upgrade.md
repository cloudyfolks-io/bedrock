# Upgrade

This page describes what an upgrade changes in cluster DNS, and what `bedrock upgrade abort` leaves behind.
The steps of an upgrade to this release are in the section "Cluster DNS" of `docs/network.md`.

## CoreDNS after `bedrock upgrade abort`

An upgrade moves cluster DNS from the CoreDNS of k0s to the CoreDNS of the release.
The `K0sUpdate` step disables the k0s CoreDNS in the k0s unit of each control plane node.
The Components phase then takes over the objects of k0s, and the `Prune` step removes the old k0s manifest directory.
`bedrock upgrade abort` does not change the unit file. The flag `--disable-components` keeps `coredns`.
Cluster DNS keeps working in every case below, because an object is never deleted and the Service keeps its address.

| Phase of the abort | State after the abort |
|---|---|
| `ControlPlane` | The control plane is restored from the backup of the upgrade. The restored cluster holds the CoreDNS objects of k0s with the stack label. The restore also puts back the k0s manifest directory. The k0s unit still disables the k0s CoreDNS, so nobody updates these objects. They keep running. |
| `Components` or later | The old operator runs again. The old release has no `coredns` group, so nobody manages the CoreDNS of the release. Its objects stay and keep running. They have no stack label and keep the checksum annotation of k0s, so k0s does not prune them and does not write its own version back. The Setting `dns.upstreams` has no effect, because the old operator does not know it. |

Check the state with these commands:

```
kubectl -n kube-system get deployment,service,configmap,poddisruptionbudget -l k8s-app=kube-dns
kubectl get cluster cluster -o jsonpath='{.metadata.annotations}'
grep disable-components /etc/systemd/system/k0scontroller.service
```

The annotation `bedrock.cloudyfolks.io/k0s-coredns-takeover` shows that the take over ran.
Run the upgrade again to give the CoreDNS to the operator of the release. The take over is safe to repeat.

To let k0s manage CoreDNS again, do these steps on each control plane node:

1. Remove `coredns` from the list of `--disable-components` in `/etc/systemd/system/k0scontroller.service`.
2. Run `systemctl daemon-reload` and `systemctl restart k0scontroller`.

k0s then writes its own CoreDNS objects over the existing ones. Do this only on a cluster that runs the old release.
