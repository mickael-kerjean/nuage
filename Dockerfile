### Build front
FROM node:12-alpine
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


### Build back
FROM golang:1.12-stretch
WORKDIR /app

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

################## Copy source
COPY Makefile /app/Makefile
COPY config /app/config
COPY server /app/server

################## Copy front
COPY webpack.config.js /app/webpack.config.js
COPY --from=0 /app/dist /app/dist

################## Prepare
ENV CGO_LDFLAGS_ALLOW '-fopenmp'
RUN mkdir -p /app/dist/data/state/
RUN cp -R config /app/dist/data/state/config
RUN make build_init
RUN find server/plugin/plg_* -type f -name '*.a' -exec mv {} /usr/local/lib/ \;
RUN go get -d ./server/...

################## Build
RUN make build_backend
RUN timeout 1 ./dist/filestash || true

################# Optional dependencies
RUN apt-get install -y curl emacs zip poppler-utils > /dev/null

################# Minimal latex dependencies for org-mode
RUN cd && apt-get install -y wget perl > /dev/null && \
    export CTAN_REPO="http://mirror.las.iastate.edu/tex-archive/systems/texlive/tlnet" && \
    curl -sL "https://yihui.name/gh/tinytex/tools/install-unx.sh" | sh && \
    mv ~/.TinyTeX /usr/share/tinytex && \
    /usr/share/tinytex/bin/x86_64-linux/tlmgr install wasy && \
    /usr/share/tinytex/bin/x86_64-linux/tlmgr install ulem && \
    /usr/share/tinytex/bin/x86_64-linux/tlmgr install marvosym && \
    /usr/share/tinytex/bin/x86_64-linux/tlmgr install wasysym && \
    /usr/share/tinytex/bin/x86_64-linux/tlmgr install xcolor && \
    /usr/share/tinytex/bin/x86_64-linux/tlmgr install listings && \
    /usr/share/tinytex/bin/x86_64-linux/tlmgr install parskip && \
    /usr/share/tinytex/bin/x86_64-linux/tlmgr install float && \
    /usr/share/tinytex/bin/x86_64-linux/tlmgr install wrapfig && \
    /usr/share/tinytex/bin/x86_64-linux/tlmgr install sectsty && \
    ln -s /usr/share/tinytex/bin/x86_64-linux/pdflatex /usr/local/bin/pdflatex && \
    apt-get purge -y --auto-remove perl wget

################## Set right and user
RUN useradd filestash && \
    chown -R filestash:filestash /app/ && \
    chmod 777 /app/

################# Cleanup
RUN find /usr/share/ -name 'doc' | xargs rm -rf && \
    find /usr/share/emacs -name '*.pbm' | xargs rm -f && \
    find /usr/share/emacs -name '*.png' | xargs rm -f && \
    find /usr/share/emacs -name '*.xpm' | xargs rm -f
RUN rm -rf /var/lib/apt/lists/* && \
    rm -rf /tmp/*

################# Run
RUN timeout 1 ./dist/filestash | grep -q start

EXPOSE 8334
VOLUME ["/app/data/"]
USER filestash
CMD ["./dist/filestash"]