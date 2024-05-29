#!/usr/bin/env bash

set -e

CHART_VERSION=$1

for c in charts/* ; do
    if [ -d "$c" ]; then
        helm dep up "$c"
        
        if [ "$c" == "charts/alloydb-omni-operator" ]; then
            # alloydb omni operator chart needs to be pulled from the GCS bucket
            helm package "$c"
        else
            helm package "$c" --version ${CHART_VERSION}
        fi
    fi
done