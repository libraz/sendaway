# sshd build host for the end-to-end tests. The sendaway binary is
# bind-mounted at /usr/local/bin/sendaway when the container starts.
FROM debian:stable-slim

RUN apt-get update \
 && apt-get install -y --no-install-recommends openssh-server rsync git make procps iptables \
 && rm -rf /var/lib/apt/lists/* \
 && useradd --create-home --shell /bin/sh --password '*' sa \
 && install -d -m 0755 /run/sshd \
 && ssh-keygen -A

COPY sshd_config /etc/ssh/sshd_config

EXPOSE 22

# The container stops on its own if a test run dies without removing it.
CMD ["timeout", "1800", "/usr/sbin/sshd", "-D", "-e"]
