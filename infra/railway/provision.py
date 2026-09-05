#!/usr/bin/env python3
"""Provision the chat-platform stack on Railway via the GraphQL API.

Uses curl (not urllib) because Railway's API is fronted by Cloudflare, which
1010-blocks Python-urllib's user-agent. Idempotent-ish: services are matched by
name so re-runs don't duplicate. Reads RAILWAY_TOKEN from the environment.

Stages (pass as argv, default "datastores"):
  datastores  create config/cell Postgres, Valkey, Kafka + their vars + volumes
  apps        create router/control/api/ws/worker from the GitHub repo + vars
  deploy      trigger a deploy of every service
  status      print services + their latest deploy status
"""
import json, os, subprocess, sys

TOKEN = os.environ["RAILWAY_TOKEN"]
PROJECT = os.environ.get("PROJECT", "7f2c7ca1-2d0c-4169-b0d8-cf7af9ae5f49")
ENVID = os.environ.get("ENVID", "19d0e0e0-4758-4f48-a92d-bef279f9d898")
REPO = os.environ.get("REPO", "darkoatanasovski/chat")
API = "https://backboard.railway.com/graphql/v2"

PG = {"POSTGRES_USER": "chat", "POSTGRES_PASSWORD": "chat", "POSTGRES_DB": "chat"}
AUTH_SECRET = os.environ.get("AUTH_SECRET", "dev-local-only-secret-change-me")
APP_SECRET_ENCRYPTION_KEY = os.environ.get("APP_SECRET_ENCRYPTION_KEY", "")


def gql(query, variables=None):
    body = json.dumps({"query": query, "variables": variables or {}})
    out = subprocess.run(
        ["curl", "-sS", "--max-time", "40", "-X", "POST", API,
         "-H", "Authorization: Bearer " + TOKEN,
         "-H", "Content-Type: application/json",
         "-A", "chat-provisioner/1.0", "--data", "@-"],
        input=body, capture_output=True, text=True).stdout
    try:
        d = json.loads(out)
    except json.JSONDecodeError:
        raise SystemExit("non-JSON from Railway: " + out[:300])
    if d.get("errors"):
        raise SystemExit("GraphQL error: " + json.dumps(d["errors"])[:400])
    return d["data"]


def services():
    q = "query($id:String!){ project(id:$id){ services{ edges{ node{ id name } } } } }"
    d = gql(q, {"id": PROJECT})
    return {e["node"]["name"]: e["node"]["id"] for e in d["project"]["services"]["edges"]}


def ensure_service(name, source):
    existing = services()
    if name in existing:
        print(f"  = {name} (exists)")
        return existing[name]
    q = "mutation($input:ServiceCreateInput!){ serviceCreate(input:$input){ id } }"
    sid = gql(q, {"input": {"projectId": PROJECT, "name": name, "source": source}})["serviceCreate"]["id"]
    print(f"  + {name}")
    return sid


def set_vars(sid, kv):
    q = "mutation($input:VariableCollectionUpsertInput!){ variableCollectionUpsert(input:$input) }"
    gql(q, {"input": {"projectId": PROJECT, "environmentId": ENVID, "serviceId": sid, "variables": kv}})


def ensure_volume(sid, mount):
    q = ("mutation($input:VolumeCreateInput!){ volumeCreate(input:$input){ id } }")
    try:
        gql(q, {"input": {"projectId": PROJECT, "environmentId": ENVID, "serviceId": sid, "mountPath": mount}})
        print(f"    volume {mount}")
    except SystemExit as e:
        print(f"    volume {mount}: {e}")


def internal(name):
    return f"{name}.railway.internal"


def set_start_command(sid, cmd):
    q = ("mutation($e:String!,$s:String!,$input:ServiceInstanceUpdateInput!){"
         " serviceInstanceUpdate(environmentId:$e, serviceId:$s, input:$input) }")
    gql(q, {"e": ENVID, "s": sid, "input": {"startCommand": cmd}})


def deploy_service(sid):
    q = "mutation($e:String!,$s:String!){ serviceInstanceDeployV2(environmentId:$e, serviceId:$s) }"
    try:
        gql(q, {"e": ENVID, "s": sid})
    except SystemExit as e:
        print(f"    deploy: {e}")


def datastores():
    print("datastores:")
    cfg = ensure_service("config-postgres", {"image": "postgres:16-alpine"})
    set_vars(cfg, PG); ensure_volume(cfg, "/var/lib/postgresql/data")

    cell = ensure_service("us-east-1-a-postgres", {"image": "postgres:16-alpine"})
    set_vars(cell, PG); ensure_volume(cell, "/var/lib/postgresql/data")

    valkey = ensure_service("us-east-1-a-valkey", {"image": "valkey/valkey:7.2-alpine"})

    kafka = ensure_service("us-east-1-a-kafka", {"image": "apache/kafka:3.8.0"})
    set_vars(kafka, {
        "KAFKA_NODE_ID": "1", "KAFKA_PROCESS_ROLES": "broker,controller",
        "KAFKA_LISTENERS": "PLAINTEXT://:9092,CONTROLLER://:9093",
        "KAFKA_ADVERTISED_LISTENERS": f"PLAINTEXT://{internal('us-east-1-a-kafka')}:9092",
        "KAFKA_CONTROLLER_LISTENER_NAMES": "CONTROLLER",
        "KAFKA_LISTENER_SECURITY_PROTOCOL_MAP": "CONTROLLER:PLAINTEXT,PLAINTEXT:PLAINTEXT",
        "KAFKA_CONTROLLER_QUORUM_VOTERS": f"1@{internal('us-east-1-a-kafka')}:9093",
        "KAFKA_OFFSETS_TOPIC_REPLICATION_FACTOR": "1",
        "KAFKA_TRANSACTION_STATE_LOG_REPLICATION_FACTOR": "1",
        "KAFKA_TRANSACTION_STATE_LOG_MIN_ISR": "1",
        "KAFKA_AUTO_CREATE_TOPICS_ENABLE": "true", "KAFKA_NUM_PARTITIONS": "6",
        "CLUSTER_ID": "Q2hhdFBsYXRmb3JtS1JhZnQx",
    })
    print("datastores done.")


def dsn(host):
    return f"postgres://chat:chat@{internal(host)}:5432/chat"


# RAILWAY_DOCKERFILE_PATH points Railway at our non-root Dockerfile so it
# builds the single chat image; the role is the start command (chat <role>).
DOCKERFILE = {"RAILWAY_DOCKERFILE_PATH": "deploy/docker/Dockerfile"}
CELL_ENV = {
    "REGION": "us-east-1", "SHARD_ID": "us-east-1-a",
    "CONFIG_DSN": dsn("config-postgres"), "CELL_DSN": dsn("us-east-1-a-postgres"),
    "VALKEY_ADDR": f"{internal('us-east-1-a-valkey')}:6379",
    "KAFKA_BROKERS": f"{internal('us-east-1-a-kafka')}:9092",
    "AUTH_SECRET": AUTH_SECRET, "APP_SECRET_ENCRYPTION_KEY": APP_SECRET_ENCRYPTION_KEY,
    "TIERS_CONFIG": "/etc/chat/tiers.yaml", "TOPOLOGY_CONFIG": "/etc/chat/topology.yaml",
    "HTTP_ADDR": ":8080", "METRICS_ADDR": ":9100", **DOCKERFILE,
}


def apps():
    print("apps (from GitHub repo):")
    src = {"repo": REPO}
    router = ensure_service("router", src)
    set_vars(router, {
        "CONFIG_DSN": dsn("config-postgres"), "VALKEY_ADDR": f"{internal('us-east-1-a-valkey')}:6379",
        "AUTH_SECRET": AUTH_SECRET, "TOPOLOGY_CONFIG": "/etc/chat/topology.yaml",
        "CONTROL_URL": f"http://{internal('control')}:8080",
        "ROUTER_ADDR": ":8080", "METRICS_ADDR": ":9100", **DOCKERFILE,
    })
    set_start_command(router, "router")

    control = ensure_service("control", src)
    set_vars(control, {**CELL_ENV, "SHARD_US_EAST_1_A_DSN": dsn("us-east-1-a-postgres")})
    set_start_command(control, "control")

    api = ensure_service("api", src); set_vars(api, CELL_ENV); set_start_command(api, "api")
    ws = ensure_service("ws", src); set_vars(ws, {**CELL_ENV, "KAFKA_CONSUMER_GROUP": "ws-us-east-1-a"}); set_start_command(ws, "ws")
    worker = ensure_service("worker", src); set_vars(worker, CELL_ENV); set_start_command(worker, "worker")
    print("apps configured (dockerfile path + role start command + env).")


def deploy():
    print("deploying all services:")
    for name, sid in services().items():
        deploy_service(sid)
        print(f"  ▶ {name}")


def status():
    for name, sid in services().items():
        print(f"  {name}: {sid}")


if __name__ == "__main__":
    stage = sys.argv[1] if len(sys.argv) > 1 else "datastores"
    {"datastores": datastores, "apps": apps, "deploy": deploy, "status": status}.get(stage, datastores)()
