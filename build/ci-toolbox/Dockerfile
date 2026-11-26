FROM python:3.14-slim

RUN apt-get update -qq \
    && apt-get upgrade -y -qq \
    && apt-get install -y -qq --no-install-recommends \
       skopeo curl git ca-certificates \
    && rm -rf /var/lib/apt/lists/*

ARG HELM_VERSION=4.3.2
RUN curl -fsSL "https://get.helm.sh/helm-v${HELM_VERSION}-linux-amd64.tar.gz" \
      | tar xz -C /tmp \
    && mv /tmp/linux-amd64/helm /usr/local/bin/helm \
    && rm -rf /tmp/linux-amd64 \
    && helm version --short

# pip is only needed to build the image; removing it also drops the packages it vendors.
RUN pip install --no-cache-dir --upgrade pip setuptools \
    && pip install --no-cache-dir pyyaml==6.0.4 requests==2.34.2 certifi \
    && python -m pip uninstall -y pip
