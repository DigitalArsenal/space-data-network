FROM --platform=$BUILDPLATFORM ubuntu:24.04 AS init
RUN apt-get update && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
    systemd systemd-sysv dbus python3 && rm -rf /var/lib/apt/lists/*

FROM ubuntu:24.04
RUN apt-get update && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
    systemd systemd-sysv dbus curl ca-certificates procps python3 && \
    rm -rf /var/lib/apt/lists/*
# Native systemd launches emulated release binaries through the kernel handler.
# Keep the target architecture's libraries alongside the native init libraries.
COPY --from=init /usr/lib/ /usr/lib/
COPY --from=init /usr/bin/python3.12 /usr/bin/python3.12
# Container init must not replace the host kernel's binary-format handlers.
RUN ln -sf /dev/null /etc/systemd/system/systemd-binfmt.service && \
    ln -sf /dev/null /etc/systemd/system/proc-sys-fs-binfmt_misc.mount
ENV container=docker
STOPSIGNAL SIGRTMIN+3
CMD ["/lib/systemd/systemd", "--system", "--unit=multi-user.target"]
