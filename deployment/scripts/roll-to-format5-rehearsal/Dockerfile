FROM ubuntu:22.04
# Supply Ubuntu packages and their dependencies for the selected architecture.
# Image assembly performs no package-repository requests.
COPY packages/ /packages/
RUN dpkg -i /packages/*.deb && \
    test -x /lib/systemd/systemd && command -v curl && command -v python3
ENV container=docker
STOPSIGNAL SIGRTMIN+3
CMD ["/lib/systemd/systemd", "--system", "--unit=multi-user.target"]
