#################################### Build front
FROM node:12-alpine AS buildfront
RUN mkdir -p /app
WORKDIR /app

################## Copy source
COPY package.json /app/package.json
COPY webpack.config.js /app/webpack.config.js
COPY .babelrc /app/.babelrc
COPY client /app/client

################## Prepare
RUN apk add git > /dev/null
RUN npm install
ENV NODE_ENV production

################## Build
RUN npm run build

# #################################### Build back
FROM golang:1.12-stretch AS buildback
WORKDIR /usr/local/go/src/github.com/mickael-kerjean/filestash

################## Build-time metadata as defined at http://label-schema.org
ARG BUILD_DATE
ARG VCS_REF
ARG VERSION
LABEL org.label-schema.build-date=$BUILD_DATE \
    org.label-schema.name="Filestash" \
    org.label-schema.description="An app to manage your files in the cloud" \
    org.label-schema.url="https://filestash.app" \
    org.label-schema.vcs-ref=$VCS_REF \
    org.label-schema.vcs-url="https://github.com/mickael-kerjean/filestash" \
    org.label-schema.vendor="Filestash, Inc." \
    org.label-schema.version=$VERSION \
    org.label-schema.schema-version="1.0"
LABEL maintainer="mickael@kerjean.me"

################## Update machine
RUN apt-get update > /dev/null

################## Install dep
RUN apt-get install -y libglib2.0-dev curl make > /dev/null

################# Optional dependencies
RUN apt-get install -y curl emacs zip poppler-utils > /dev/null

################# Minimal latex dependencies for org-mode
RUN apt-get install -y wget perl > /dev/null
RUN export CTAN_REPO="http://mirror.las.iastate.edu/tex-archive/systems/texlive/tlnet"

################# install tinytex dep
RUN curl -sL "https://yihui.name/gh/tinytex/tools/install-unx.sh" | sh
RUN mv ~/.TinyTeX /usr/share/tinytex && \
    /usr/share/tinytex/bin/x86_64-linux/tlmgr install wasy && \
    /usr/share/tinytex/bin/x86_64-linux/tlmgr install ulem && \
    /usr/share/tinytex/bin/x86_64-linux/tlmgr install marvosym && \
    /usr/share/tinytex/bin/x86_64-linux/tlmgr install wasysym && \
    /usr/share/tinytex/bin/x86_64-linux/tlmgr install xcolor && \
    /usr/share/tinytex/bin/x86_64-linux/tlmgr install listings && \
    /usr/share/tinytex/bin/x86_64-linux/tlmgr install parskip && \
    /usr/share/tinytex/bin/x86_64-linux/tlmgr install float && \
    /usr/share/tinytex/bin/x86_64-linux/tlmgr install wrapfig && \
    /usr/share/tinytex/bin/x86_64-linux/tlmgr install sectsty

################## make symlink for pdflatex
RUN ln -s /usr/share/tinytex/bin/x86_64-linux/pdflatex /usr/local/bin/pdflatex

################## copy filestash backend source
COPY main.go main.go
COPY src src
COPY Makefile Makefile

################## Download dependencies

RUN make backend_download

################## Install dependencies
RUN make backend_install_dep

RUN make backend_install

################## Build
RUN make backend_build

################## Copy filestash front builded
COPY --from=buildfront /app/dist ./dist
COPY config/config.json ./dist/data/state/config/config.json
COPY config/mime.json ./dist/data/state/config/mime.json
COPY config/emacs.el ./dist/data/state/config/emacs.el
COPY config/ox-gfm.el ./dist/data/state/config/ox-gfm.el

################# Test Run && test Front
RUN timeout 2 ./dist/filestash | grep -q starting | wget -qO- localhost:8334/about | grep Filestash

################## Set right and user
RUN useradd filestash && \
    chown -R filestash:filestash ./dist

################# Cleanup
RUN find /usr/share/ -name 'doc' | xargs rm -rf && \
    find /usr/share/emacs -name '*.pbm' | xargs rm -f && \
    find /usr/share/emacs -name '*.png' | xargs rm -f && \
    find /usr/share/emacs -name '*.xpm' | xargs rm -f
RUN apt-get purge -y --auto-remove perl wget
RUN rm -rf /var/lib/apt/lists/* && \
    rm -rf /tmp/*

EXPOSE 8334
VOLUME ["/usr/local/go/src/github.com/mickael-kerjean/dist/data/"]
USER filestash
CMD ["./dist/filestash"]