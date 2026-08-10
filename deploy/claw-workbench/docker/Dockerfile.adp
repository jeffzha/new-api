ARG NODE_BUILD_IMAGE=docker.m.daocloud.io/library/node:22-bookworm-slim
ARG PYTHON_BUILD_IMAGE=docker.m.daocloud.io/library/python:3.12-slim

FROM ${NODE_BUILD_IMAGE} AS frontend
ARG NPM_REGISTRY=https://registry.npmmirror.com
WORKDIR /src
RUN mkdir -p /src/server
COPY --from=adp_source client/ /src/client/
RUN cd /src/client \
    && npm config set registry "${NPM_REGISTRY}" \
    && npm ci --no-audit --no-fund \
    && npm run build

FROM ${PYTHON_BUILD_IMAGE} AS python-build
ARG PIP_INDEX_URL=https://mirrors.aliyun.com/pypi/simple/
ENV PIP_INDEX_URL=${PIP_INDEX_URL} \
    UV_DEFAULT_INDEX=${PIP_INDEX_URL} \
    UV_PROJECT_ENVIRONMENT=/opt/venv
WORKDIR /build
COPY --from=adp_source server/pyproject.toml server/uv.lock ./
RUN python -m pip install --no-cache-dir uv==0.9.2 \
    && uv sync --frozen --no-dev

FROM ${PYTHON_BUILD_IMAGE} AS runtime
ENV PATH=/opt/venv/bin:$PATH \
    PYTHONDONTWRITEBYTECODE=1 \
    PYTHONUNBUFFERED=1
RUN apt-get update \
    && apt-get upgrade -y \
    && rm -rf /var/lib/apt/lists/* \
    && groupadd --system --gid 10001 workbench \
    && useradd --system --uid 10001 --gid 10001 --home-dir /app --shell /usr/sbin/nologin workbench
WORKDIR /app
COPY --from=python-build /opt/venv /opt/venv
COPY --from=adp_source server/ /app/
COPY --from=frontend /src/server/static/ /app/static/
COPY deploy/claw-workbench/docker/entrypoint-adp.sh /app/entrypoint.sh
RUN mkdir -p /app/logs \
    && chown -R 10001:10001 /app/logs \
    && chmod 0555 /app/entrypoint.sh
USER 10001:10001
EXPOSE 8000
ENTRYPOINT ["/app/entrypoint.sh"]
