FROM alpine/helm:latest as builder
ARG CHART_VERSION=1.0.0
COPY charts charts
COPY scripts/ /usr/local/bin/
RUN chmod +x /usr/local/bin/build.sh
RUN build.sh ${CHART_VERSION}

FROM cgr.dev/chainguard/bash:latest
COPY --from=builder /apps/alloydb-omni-*.tgz ./