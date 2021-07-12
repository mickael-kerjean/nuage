import React from "react";
import Path from "path";

import { Files } from "../model/";
import { confirm, notify, upload } from "../helpers/";
import { Icon, NgIf } from "./";
import { t } from "../locales/";
import "./upload_queue.scss";


function humanFileSize(bytes, si) {
    var thresh = si ? 1000 : 1024;
    if (Math.abs(bytes) < thresh) {
        return bytes.toFixed(1) + " B";
    }
    var units = si
        ? ["kB", "MB", "GB", "TB", "PB", "EB", "ZB", "YB"]
        : ["KiB", "MiB", "GiB", "TiB", "PiB", "EiB", "ZiB", "YiB"];
    var u = -1;
    do {
        bytes /= thresh;
        ++u;
    } while (Math.abs(bytes) >= thresh && u < units.length - 1);
    return bytes.toFixed(1) + " " + units[u];
}

function waitABit() {
    return new Promise((done) => {
        window.setTimeout(() => {
            requestAnimationFrame(() => {
                done();
            });
        }, 200);
    });
}

export class UploadQueue extends React.Component {
    constructor(props) {
        super(props);
        this.state = {
            timeout: 1,
            running: false,
            files: [],
            processes: [],
            currents: [],
            failed: [],
            finished: [],
            prior_status: {},
            progress: {},
            speed: [],
        };
    }

    componentDidMount() {
        if (typeof this.state.timeout === "number") {
            this.setState({
                timeout: window.setTimeout(() => {
                    this.componentDidMount();
                }, Math.random() * 1000 + 200)
            });
        }
        upload.subscribe((path, files) => this.addFiles(path, files));
    }

    componentWillUnmount() {
        window.clearTimeout(this.state.timeout);
    }

    reset() {
        this.setState({
            files: [],
            processes: [],
            currents: [],
            failed: [],
            finished: [],
            prior_status: {},
            progress: {},
            speed: [],
            error: null,
        });
    }

    emphasis(path) {
        if(!path) return;
        else if(path[0] === "/") path = path.slice(1);

        if(navigator && navigator.clipboard){
            navigator.clipboard.writeText(path);
            notify.send(t("Copied to clipboard"), "info");
        }
    }

    runner(id) {
        let current_process = null;
        let processes = [...this.state.processes];
        if (processes.length === 0 || !this.state.running) {
            return Promise.resolve();
        }

        var i;
        for (i = 0; i < processes.length; i++) {
            if (
                // init: getting started with creation of files/folders
                processes[i].parent === null ||
                // running: make sure we've created the parent folder
                this.state.prior_status[processes[i].parent] === true
            ) {
                current_process = this.state.processes[i];
                processes.splice(i, 1);
                this.setState({
                    processes,
                    currents: [...this.state.currents, current_process]
                });
                break;
            }
        }

        if (current_process) {
            return current_process.fn(id)
                .then(() => {
                    if (current_process.id) {
                        this.setState({
                            prior_status: {
                                ...this.state.prior_status,
                                [current_process.id]: true
                            }
                        });
                    }
                    this.setState({
                        currents: this.state.currents.filter((c) => c.path != current_process.path),
                        finished: [...this.state.finished, current_process],
                        error: null
                    });
                    return this.runner(id);
                })
                .catch((err) => {
                    current_process.err = err;
                    this.setState({
                        failed: [...this.state.failed, current_process],
                        currents: this.state.currents.filter((c) => c.path != current_process.path),
                    });
                    let { message } = err;
                    if (message !== "aborted") {
                        this.setState({ error: err && err.message });
                    }
                    return this.runner(id);
                });
        } else {
            return waitABit().then(() => this.runner(id));
        }
    }

    updateProgress(path, e) {
        if (e.lengthComputable) {
            let prev = this.state.progress[path];
            this.setState({
                progress: {
                    ...this.state.progress,
                    [path]: {
                        ...prev,
                        percent: Math.round(100 * e.loaded / e.total),
                        loaded: e.loaded,
                        time: Date.now(),
                        prev: prev ? prev : null,
                    }
                }
            });
        }
    }

    updateAbort(path, abort) {
        this.setState({
            progress: {
                ...this.state.progress,
                [path]: {
                    ...this.state.progress[path],
                    abort,
                },
            }
        });
    }

    addFiles(path, files) {
        const processes = files.map((file) => {
            let original_path = file.path;
            file.path = Path.join(path, file.path);
            if (file.type === "file") {
                if (files.length < 150) Files.touch(file.path, file.file, "prepare_only");
                return {
                    path: original_path,
                    parent: file._prior || null,
                    fn: Files.touch.bind(
                        Files, file.path, file.file, "execute_only",
                        {
                            progress: (e) => this.updateProgress(original_path, e),
                            abort: (x) => this.updateAbort(original_path, x),
                        }
                    )
                };
            } else {
                Files.mkdir(file.path, "prepare_only");
                return {
                    id: file._id || null,
                    path: original_path,
                    parent: file._prior || null,
                    fn: Files.mkdir.bind(Files, file.path, "execute_only")
                };
            }
        });

        this.setState({
            processes: [...this.state.processes, ...processes],
            files: [...this.state.files, ...files],
        });
        this.start();
    }

    retryFiles(process) {
        this.setState({
            processes: [...this.state.processes, process],
            failed: this.state.failed.filter((c) => c.path != process.path),
            error: null,
        });
        requestAnimationFrame(() => this.start());
    }

    maxPoolSize() {
        return window.CONFIG["upload_pool_size"] || 10;
    }

    start() {
        if (!this.state.running) {
            window.setTimeout(() => this.calcSpeed(), 500);
            this.setState({
                running: true,
                error: null
            });
            Promise.all(Array.apply(null, Array(this.maxPoolSize())).map(() => {
                return this.runner();
            })).then(() => {
                this.setState({ running: false });
            }).catch((err) => {
                notify.send(err, "error");
                this.setState({ running: false, error: err && err.message });
            });
        }
    }

    abort(p) {
        let info = this.state.progress[p.path];
        if (info && info.abort) {
            info.abort();
        }
    }

    getCurrentPercent(path) {
        let info = this.state.progress[path];
        if (info && info.percent) {
            return this.state.progress[path].percent + "%";
        }
        return "0%";
    }

    calcSpeed() {
        let now = Date.now();
        let curSpeed = [];
        for (const [, value] of Object.entries(this.state.progress)) {
            if (value.prev && now - value.time < 5 * 1000) {
                let bytes = value.loaded - value.prev.loaded;
                let timeMs = value.time - value.prev.time;
                curSpeed.push(1000 * bytes / timeMs);
            }
        }
        let avgSpeed = curSpeed.reduce(function (p, c, i) { return p + (c - p) / (i + 1); }, 0);
        this.setState({
            speed: [...this.state.speed, avgSpeed].slice(-5)
        });
        if (this.state.running) {
            window.setTimeout(() => this.calcSpeed(), 500);
        }
    }

    getState() {
        let avgSpeed = this.state.speed.reduce(function (p, c, i) { return p + (c - p) / (i + 1); }, 0);
        let speedStr = "";
        if (avgSpeed > 0) {
            speedStr = " ~ " + humanFileSize(avgSpeed) + "/s";
        }
        if (this.state.running) {
            return t("Running")+"..." + speedStr;
        }
        return t("Done") + speedStr;
    }

    onClose() {
        if(this.state.running) {
            confirm.now(
                t("Abort current uploads?"),
                () => {
                    this.setState({
                        running: false,
                    });
                    this.state.currents.map(p => this.abort(p));
                    window.requestAnimationFrame(() => this.reset(), 30);
                },
                () => {}
            );
        } else  {
            this.reset();
        }
    }

    renderRows(arr, state, col_state, action) {
        let row_class = state + "_color";
        return arr.slice(0, 1000).map((process, i) => {
            return (
                <div className={"file_row " + row_class} key={i}>
                    <div
                        className="file_path"
                        onClick={() => this.emphasis(process.path)}
                    >
                        {process.path.replace(/\//, "")}
                    </div>
                    {col_state(process)}
                    <div className="file_control">
                        {action ? action(process): (<span></span>)}
                    </div>
                </div>
            );
        });
    }

    render() {
        let { finished, files, processes, currents, failed } = this.state;
        let totalFiles = files.length;
        return (
            <NgIf cond={totalFiles > 0}>
                <div className="component_upload_queue">
                    <h2>
                        { t("CURRENT UPLOAD") }
                        <div className="count_block">
                            <span className="completed">{finished.length}</span>
                            <span className="grandTotal">{totalFiles}</span>
                        </div>
                        <Icon name="close" onClick={() => this.onClose()} />
                    </h2>
                    <h3>{this.state.error ? this.state.error : this.getState()}</h3>
                    <div className="stats_content">
                        {this.renderRows(
                            finished,
                            "done",
                            () => (<div className="file_state file_state_done">{ t("Done") }</div>),
                        )}
                        {this.renderRows(
                            currents,
                            "current",
                            (p) => (
                                <div className="file_state file_state_current">
                                    {this.getCurrentPercent(p.path)}
                                </div>
                            ),
                            (p) => (
                                <Icon name="stop" onClick={() => this.abort(p)} ></Icon>
                            )
                        )}
                        {this.renderRows(
                            processes,
                            "todo",
                            () => (
                                <div className="file_state file_state_todo">{ t("Waiting") }</div>
                            )
                        )}
                        {this.renderRows(
                            failed,
                            "error",
                            (p) => (
                                (p.err && p.err.message == "aborted")
                                ?
                                    <div className="file_state file_state_error">{ t("Aborted") }</div>
                                :
                                <div className="file_state file_state_error">{ t("Error") }</div>
                            ),
                            (p) => (
                                <Icon name="refresh" onClick={() => this.retryFiles(p)} ></Icon>
                            )
                        )}
                    </div>
                </div>
            </NgIf>
        );
    }
}
