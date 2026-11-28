#!/usr/bin/env python3
"""Render the disposable single-instance backup policies from EndpointSlice JSON."""

import argparse
from ipaddress import IPv4Address
import json
import re
import sys


API = ("default", "kubernetes")
RGW = ("rook-ceph", "rook-ceph-rgw-bedrock-rgw")
PORT_NAMES = {API: "https", RGW: "http"}
UID = re.compile(r"[0-9a-f]{8}(?:-[0-9a-f]{4}){3}-[0-9a-f]{12}")


def object_field(value, field):
    if not isinstance(value, dict):
        raise ValueError(f"{field} must be an object")
    return value


def list_field(value, field):
    if not isinstance(value, list):
        raise ValueError(f"{field} must be an array")
    return value


def ready_addresses(item, service):
    for endpoint in list_field(item.get("endpoints"), f"{service} endpoints"):
        endpoint = object_field(endpoint, f"{service} endpoint")
        conditions = endpoint.get("conditions")
        ready = None if conditions is None else object_field(conditions, "conditions").get("ready")
        if ready is False:
            continue
        if ready is not None and ready is not True:
            raise ValueError(f"{service} readiness must be boolean or null")
        addresses = list_field(endpoint.get("addresses"), f"{service} addresses")
        if not addresses:
            raise ValueError(f"{service} ready endpoint has no IPv4 address")
        for value in addresses:
            try:
                if not isinstance(value, str):
                    raise ValueError
                address = IPv4Address(value)
            except ValueError:
                raise ValueError(f"{service} has an invalid IPv4 endpoint address") from None
            if address.is_unspecified or address.is_loopback or address.is_link_local or address.is_multicast:
                raise ValueError(f"{service} has a non-routable IPv4 endpoint address")
            if address == IPv4Address("255.255.255.255"):
                raise ValueError(f"{service} has a broadcast IPv4 endpoint address")
            yield address


def endpoint_targets(document):
    targets = {service: set() for service in PORT_NAMES}
    for item in list_field(object_field(document, "input").get("items"), "items"):
        item = object_field(item, "EndpointSlice")
        metadata = object_field(item.get("metadata"), "EndpointSlice metadata")
        labels = object_field(metadata.get("labels", {}), "EndpointSlice labels")
        namespace = metadata.get("namespace")
        name = labels.get("kubernetes.io/service-name")
        if not isinstance(namespace, str) or not isinstance(name, str):
            continue
        key = (namespace, name)
        if key not in PORT_NAMES or item.get("addressType") != "IPv4":
            continue
        service = "/".join(key)
        ports = []
        for entry in list_field(item.get("ports", []), f"{service} ports"):
            entry = object_field(entry, f"{service} port")
            if entry.get("name") != PORT_NAMES[key] or entry.get("protocol") != "TCP":
                continue
            port = entry.get("port")
            if type(port) is not int or not 1 <= port <= 65535:
                raise ValueError(f"{service} has an invalid TCP target port")
            ports.append(port)
        if len(ports) > 1:
            raise ValueError(f"{service} has an ambiguous named TCP target port")
        if ports:
            targets[key].update((ports[0], address) for address in ready_addresses(item, service))
    for key, tuples in targets.items():
        if not tuples:
            raise ValueError(f"no ready IPv4 TCP targets for {'/'.join(key)}:{PORT_NAMES[key]}")
    return targets


def selector(labels):
    return {"matchLabels": labels}


def rule(peers, port, *protocols):
    return {"to": peers, "ports": [{"protocol": protocol, "port": port} for protocol in protocols]}


def policy(name, labels, egress):
    return {
        "apiVersion": "networking.k8s.io/v1",
        "kind": "NetworkPolicy",
        "metadata": {
            "name": name,
            "namespace": "bedrock-dbaas",
            "annotations": {"fabric.cloudyfolks.io/network_policy_for": "fabric"},
        },
        "spec": {"podSelector": selector(labels), "policyTypes": ["Egress"], "egress": egress},
    }


def build_policies(document, uid, engine, *, deny_rgw=False):
    if not isinstance(uid, str) or not UID.fullmatch(uid):
        raise ValueError("database UID must be a lowercase Kubernetes UUID")
    if engine not in ("postgresql", "mysql"):
        raise ValueError("engine must be postgresql or mysql")
    targets = endpoint_targets(document)
    if deny_rgw and targets[API] & targets[RGW]:
        raise ValueError("API and RGW share a target; cannot deny RGW while allowing the API")
    instance = f"db-{uid}"
    engine_labels = {
        "app.kubernetes.io/instance": instance,
        "bedrock.cloudyfolks.io/database-role": "engine",
    }
    dns = rule([{
        "namespaceSelector": selector({"kubernetes.io/metadata.name": "kube-system"}),
        "podSelector": selector({"k8s-app": "kube-dns"}),
    }], 53, "UDP", "TCP")
    # The instance's backup sidecar accesses RGW. Both engines use DNS, and CNPG
    # uses the Kubernetes API; its remote plugin controller is called by the operator.
    tuples = targets[API] if deny_rgw else targets[API] | targets[RGW]
    backends = {}
    for port, address in sorted(tuples):
        backends.setdefault(port, []).append({"ipBlock": {"cidr": f"{address}/32"}})
    egress = [dns, *(rule(peers, port, "TCP") for port, peers in backends.items())]
    items = [policy(f"{instance}-e2e-egress", engine_labels, egress)]
    if engine == "mysql":
        # Percona 1.2.0 build/run-backup.sh calls SRC_NODE on TCP 6450.
        # The source is the database Pod, not HAProxy or RGW.
        source = {"podSelector": selector({**engine_labels, "app.kubernetes.io/component": "database"})}
        items.append(policy(
            f"{instance}-e2e-backup-egress",
            {"bedrock.cloudyfolks.io/backup-of": instance},
            [dns, rule([source], 6450, "TCP")],
        ))
    return {"apiVersion": "v1", "kind": "List", "items": items}


def main():
    parser = argparse.ArgumentParser(
        description=__doc__,
        epilog="Read one EndpointSlice List (with items from both Services) on stdin. "
               "Print a Kubernetes List on stdout; never apply it.",
    )
    parser.add_argument("engine", choices=("postgresql", "mysql"))
    parser.add_argument("uid", help="metadata.uid of the fresh, single-instance Database")
    parser.add_argument("--deny-rgw", action="store_true", help="omit only RGW allows for a negative probe")
    args = parser.parse_args()
    try:
        policies = build_policies(json.load(sys.stdin), args.uid, args.engine, deny_rgw=args.deny_rgw)
    except ValueError as error:
        parser.error(str(error))
    print(json.dumps(policies))


if __name__ == "__main__":
    main()
