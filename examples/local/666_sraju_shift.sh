#!/bin/bash

source ./env.sh

# topo vtctld gate
CELL=zone1 ./scripts/etcd-up.sh
CELL=zone1 ./scripts/vtctld-up.sh
CELL=zone1 ./scripts/vtgate-up.sh

for i in 300 301 302; do
 CELL=zone1 TABLET_UID=$i ./scripts/mysqlctl-up.sh
 SHARD=-80 CELL=zone1 KEYSPACE=srajucustomer TABLET_UID=$i ./scripts/vttablet-up.sh
done

for i in 400 401 402; do
 CELL=zone1 TABLET_UID=$i ./scripts/mysqlctl-up.sh
 SHARD=80- CELL=zone1 KEYSPACE=srajucustomer TABLET_UID=$i ./scripts/vttablet-up.sh
done

vtctlclient InitShardMaster -force srajucustomer/-80 zone1-300
vtctlclient InitShardMaster -force srajucustomer/80- zone1-400

vtctlclient ApplySchema -sql-file sraju/create_sraju.sql srajucustomer
vtctlclient ApplyVSchema -vschema_file sraju/sraju_vschema.json srajucustomer

for i in 500 501 502; do
 CELL=zone1 TABLET_UID=$i ./scripts/mysqlctl-up.sh
 CELL=zone1 KEYSPACE=srajucustomer TABLET_UID=$i ./scripts/vttablet-up.sh
done
vtctlclient InitShardMaster -force srajucustomer/0 zone1-500
