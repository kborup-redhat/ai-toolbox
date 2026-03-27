'use strict';

var cachedOverview = null;
var metricsModelsLoaded = false;
var analysisModelsLoaded = false;
var liveModelsLoaded = false;
var consoleURL = '';
var dashboardURL = '';
var loadTestStatusInterval = null;

document.addEventListener('DOMContentLoaded', function () {
    setupTabs();
    loadOverview();
    checkUserWorkloadMonitoring();
    document.getElementById('metrics-model-select').addEventListener('change', function () {
        if (this.value) refreshMetrics();
    });
    document.getElementById('metrics-range-select').addEventListener('change', function () {
        var modelSelect = document.getElementById('metrics-model-select');
        if (modelSelect.value) refreshMetrics();
    });
});

// ========== Tab navigation ==========

function setupTabs() {
    document.querySelectorAll('[data-tab]').forEach(function (link) {
        link.addEventListener('click', function (e) {
            e.preventDefault();
            switchTab(this.getAttribute('data-tab'));
        });
    });
}

function switchTab(tab) {
    document.querySelectorAll('[data-tab]').forEach(function (l) { l.classList.remove('pf-m-current'); });
    document.querySelector('[data-tab="' + tab + '"]').classList.add('pf-m-current');
    ['models', 'metrics', 'analysis', 'live', 'firewalls', 'status'].forEach(function (t) {
        document.getElementById('tab-' + t).classList.toggle('hidden', t !== tab);
    });
    if (tab === 'models') loadOverview();
    if (tab === 'firewalls') loadFirewalls();
    if (tab === 'metrics') loadModelList();
    if (tab === 'analysis') loadAnalysisModelList();
    if (tab === 'live') { loadLiveModelList(); fetchLoadTestBanner(); }
    if (tab === 'status') loadStatus();
    if (tab !== 'live') { stopLive(); stopLoadTestPolling(); }
}

// ========== Overview data ==========

function loadOverview() {
    fetch('/api/overview')
        .then(handleJsonResponse)
        .then(function (data) {
            cachedOverview = data;
            consoleURL = data.consoleURL || '';
            dashboardURL = data.dashboardURL || '';
            renderModels(data);
        })
        .catch(function (err) {
            hide('models-loading');
            show('models-error');
            document.getElementById('models-error-msg').textContent = 'Failed to load: ' + err.message;
        });
}

// ========== Models tab ==========

function renderModels(data) {
    hide('models-loading');
    var models = data.models || [];
    if (models.length === 0) { show('models-empty'); return; }
    show('models-content');

    var nodes = data.nodes || [];
    var readyCount = 0;
    models.forEach(function (m) { if (m.status === 'Ready') readyCount++; });

    // Break down GPUs by type from nodes
    var gpuByType = {};
    var gpuSharingModes = [];
    nodes.forEach(function (n) {
        var type = n.gpuProduct || 'GPU';
        if (!gpuByType[type]) gpuByType[type] = { total: 0, used: 0, physical: 0 };
        gpuByType[type].total += n.gpuAllocatable || 0;
        gpuByType[type].used += n.gpusUsed || 0;
        if (n.gpuPhysicalCount) gpuByType[type].physical += n.gpuPhysicalCount;
        if (n.gpuSharing && gpuSharingModes.indexOf(n.gpuSharing) === -1) gpuSharingModes.push(n.gpuSharing);
    });

    // Break down GPUs in use by type from models
    var gpuInUseByType = {};
    models.forEach(function (m) {
        var count = m.gpusRequested || 0;
        if (count === 0) return;
        var type = m.gpuProduct || 'GPU';
        gpuInUseByType[type] = (gpuInUseByType[type] || 0) + count;
    });

    // Shorten GPU product names: "NVIDIA-L4" -> "L4", "NVIDIA-A100-SXM4-80GB" -> "A100-SXM4-80GB"
    function shortGpuName(name) {
        return name.replace(/^NVIDIA-/i, '');
    }

    // Format "2x A100, 1x L40S" style labels
    function fmtGpuBreakdown(byType) {
        var parts = [];
        Object.keys(byType).forEach(function (type) {
            var count = typeof byType[type] === 'number' ? byType[type] : byType[type].count;
            if (count > 0) parts.push(count + 'x ' + shortGpuName(type));
        });
        return parts.length > 0 ? parts.join(', ') : '0';
    }

    var gpuInUseLabel = fmtGpuBreakdown(gpuInUseByType);

    // Build available label as "free / total" per type, each on its own line
    var gpuAvailLines = [];
    Object.keys(gpuByType).forEach(function (type) {
        var info = gpuByType[type];
        var avail = info.total - info.used;
        if (info.total > 0) {
            gpuAvailLines.push(avail + ' / ' + info.total + ' ' + shortGpuName(type));
        }
    });
    var gpuAvailFull = gpuAvailLines.join(', ') || '0';
    if (gpuSharingModes.length > 0) {
        gpuAvailFull += ' [' + gpuSharingModes.join(', ') + ']';
    }

    document.getElementById('models-summary').innerHTML =
        summaryCard('Total Models', models.length, 'pf-icon-server') +
        summaryCard('Models Ready', readyCount + ' / ' + models.length, 'pf-icon-ok') +
        summaryCard('GPUs in Use', gpuInUseLabel, 'pf-icon-cpu') +
        summaryCard('GPUs Available', gpuAvailFull, 'pf-icon-memory');

    var tbody = document.getElementById('models-table-body');
    tbody.innerHTML = '';
    models.forEach(function (m) {
        var tr = document.createElement('tr');
        tr.className = 'model-row'; tr.style.cursor = 'pointer';
        tr.onclick = function () { showModelDetail(m); };
        tr.innerHTML =
            '<td><strong>' + esc(m.name) + '</strong></td>' +
            '<td>' + esc(m.namespace) + '</td>' +
            '<td>' + esc(m.runtime || '-') + '</td>' +
            '<td>' + statusBadge(m.status) + '</td>' +
            '<td>' + esc(m.nodeName || '-') + '</td>' +
            '<td>' + ((m.gpusRequested || 0) > 0 ? (m.gpusRequested + 'x ' + esc(shortGpuName(m.gpuProduct || 'GPU'))) : '0') + '</td>' +
            '<td>' + fmtCoresRange(m.cpusRequested, m.cpuLimits) + '</td>' +
            '<td>' + fmtMemRange(m.memRequested, m.memLimits) + '</td>' +
            '<td>' + fmtDate(m.createdAt) + '</td>';
        tbody.appendChild(tr);
    });
}

function showModelDetail(model) {
    document.getElementById('model-detail-title').textContent = model.namespace + '/' + model.name;
    var tbody = document.getElementById('model-pods-body');
    tbody.innerHTML = '';
    var pods = model.modelPods || [];
    if (pods.length === 0) {
        tbody.innerHTML = '<tr><td colspan="6">No pods found for this model.</td></tr>';
    } else {
        pods.forEach(function (p) {
            var tr = document.createElement('tr');
            tr.innerHTML = '<td>' + esc(p.name) + '</td><td>' + esc(p.nodeName || '-') + '</td><td>' + statusBadge(p.phase) + '</td><td>' + (p.gpusRequested || 0) + '</td><td>' + fmtCoresRange(p.cpusRequested, p.cpuLimits) + '</td><td>' + fmtMemRange(p.memRequested, p.memLimits) + '</td>';
            tbody.appendChild(tr);
        });
    }
    var infoEl = document.getElementById('model-endpoint-info');
    var h = '';
    if (model.url) h += '<div class="model-info-row"><strong>Inference URL:</strong> <code>' + esc(model.url) + '</code></div>';
    if (model.runtime) h += '<div class="model-info-row"><strong>Serving Runtime:</strong> ' + esc(model.runtime) + '</div>';
    if (model.engineVersion) h += '<div class="model-info-row"><strong>Engine Version:</strong> ' + esc(model.engineVersion) + '</div>';
    if (model.maxModelLen) h += '<div class="model-info-row"><strong>Max Model Length:</strong> ' + model.maxModelLen.toLocaleString() + ' tokens</div>';
    if (model.modelRoot) h += '<div class="model-info-row"><strong>Model Path:</strong> <code>' + esc(model.modelRoot) + '</code></div>';
    if (model.gpuProduct) h += '<div class="model-info-row"><strong>GPU:</strong> ' + esc(model.gpuProduct) + '</div>';
    // Links to OpenShift AI and Console
    var links = [];
    if (dashboardURL) {
        links.push('<a href="' + esc(dashboardURL) + '/projects/' + encodeURIComponent(model.namespace) + '" target="_blank" class="pf-v5-c-button pf-m-link pf-m-inline">Open in OpenShift AI</a>');
    }
    if (consoleURL) {
        links.push('<a href="' + esc(consoleURL) + '/k8s/ns/' + encodeURIComponent(model.namespace) + '/serving.kserve.io~v1beta1~InferenceService/' + encodeURIComponent(model.name) + '" target="_blank" class="pf-v5-c-button pf-m-link pf-m-inline">Open in Console</a>');
    }
    if (links.length > 0) h += '<div class="model-info-row">' + links.join(' &nbsp;|&nbsp; ') + '</div>';
    if (model.labels) {
        var lh = Object.keys(model.labels).map(function (k) {
            return '<span class="pf-v5-c-label pf-m-outline" style="margin:2px;"><span class="pf-v5-c-label__content">' + esc(k) + '=' + esc(model.labels[k]) + '</span></span>';
        }).join(' ');
        if (lh) h += '<div class="model-info-row"><strong>Labels:</strong> ' + lh + '</div>';
    }
    infoEl.innerHTML = h;
    show('model-detail-panel');
    document.getElementById('model-detail-panel').scrollIntoView({ behavior: 'smooth', block: 'nearest' });
}
function closeModelDetail() { hide('model-detail-panel'); }

// ========== Metrics tab ==========

function loadModelList() {
    fetch('/api/models/list')
        .then(handleJsonResponse)
        .then(function (models) {
            metricsModelsLoaded = true;
            var sel = document.getElementById('metrics-model-select');
            var prev = sel.value;
            sel.innerHTML = '<option value="">-- Choose a model --</option>';
            (models || []).forEach(function (m) {
                var opt = document.createElement('option');
                opt.value = m.namespace + '/' + m.name;
                opt.textContent = m.namespace + ' / ' + m.name + (m.runtime ? ' (' + m.runtime + ')' : '') + (m.status ? ' [' + m.status + ']' : '');
                sel.appendChild(opt);
            });
            if (prev) sel.value = prev;
        })
        .catch(function (err) {
            showMetricsError('Failed to load model list: ' + err.message);
        });
}

function refreshMetrics() {
    var sel = document.getElementById('metrics-model-select');
    var val = sel.value;
    if (!val) return;
    var parts = val.split('/');
    var namespace = parts[0], model = parts[1];
    var range = document.getElementById('metrics-range-select').value;

    hide('metrics-placeholder'); hide('metrics-content'); hide('metrics-error');
    show('metrics-loading');

    fetch('/api/metrics?model=' + encodeURIComponent(model) + '&namespace=' + encodeURIComponent(namespace) + '&range=' + encodeURIComponent(range))
        .then(handleJsonResponse)
        .then(function (data) {
            hide('metrics-loading');
            renderMetrics(data);
            show('metrics-content');
        })
        .catch(function (err) {
            hide('metrics-loading');
            showMetricsError('Failed to load metrics: ' + err.message);
        });
}

function renderMetrics(data) {
    var s = data.summary || {};
    var series = data.series || {};

    // Summary cards with hit rate and usage
    var rps = fmtNum(s.requestsPerSec, 2);
    var totalReq = fmtNum(s.requestsTotal, 0);
    var avgLat = s.avgLatencyMs ? fmtNum(s.avgLatencyMs, 1) + ' ms' : '-';
    var p99Lat = s.p99LatencyMs ? fmtNum(s.p99LatencyMs, 1) + ' ms' : '-';
    var errRate = (s.errorRate !== '' && s.errorRate != null) ? parseFloat(s.errorRate).toFixed(1) + '%' : '0%';
    var tps = fmtNum(s.tokensPerSec, 1);
    var activeReq = (s.activeRequests !== '' && s.activeRequests != null) ? s.activeRequests : '-';
    var gpuUtil = s.gpuUtilization ? fmtNum(s.gpuUtilization, 1) + '%' : '-';
    var kvCache = s.kvCacheUsage ? (parseFloat(s.kvCacheUsage) * 100).toFixed(1) + '%' : '-';
    var gpuMem = '-';
    if (s.gpuMemoryUsed && s.gpuMemoryTotal) {
        gpuMem = fmtBytes(s.gpuMemoryUsed) + ' / ' + fmtBytes(s.gpuMemoryTotal);
    } else if (s.gpuMemoryUsed) {
        gpuMem = fmtBytes(s.gpuMemoryUsed);
    }
    var cpuUse = s.cpuUsage ? fmtNum(s.cpuUsage, 2) + ' cores' : '-';
    var memUse = s.memoryUsage ? fmtBytes(s.memoryUsage) : '-';

    // Total tokens generated (from Prometheus counter)
    var totalTokens = '-';
    if (s.totalTokens) {
        var tt = parseFloat(s.totalTokens);
        if (!isNaN(tt) && tt > 0) totalTokens = fmtLargeNum(tt);
    }

    document.getElementById('metrics-summary').innerHTML =
        summaryCard('Hit Rate', rps ? rps + ' req/s' : '-', 'pf-icon-trend-up') +
        summaryCard('Total Requests', totalReq || '-', 'pf-icon-server') +
        summaryCard('Active Requests', activeReq, 'pf-icon-running') +
        summaryCard('Avg Latency', avgLat, 'pf-icon-clock') +
        summaryCard('P99 Latency', p99Lat, 'pf-icon-clock') +
        summaryCard('Error Rate', errRate, 'pf-icon-error-circle-o') +
        summaryCard('Token Throughput', tps ? tps + ' tok/s' : '-', 'pf-icon-bolt') +
        summaryCard('Total Tokens', totalTokens, 'pf-icon-catalog') +
        summaryCard('GPU Utilization', gpuUtil, 'pf-icon-cpu') +
        summaryCard('KV Cache Usage', kvCache, 'pf-icon-memory') +
        summaryCard('GPU Memory', gpuMem, 'pf-icon-memory') +
        summaryCard('CPU Usage', cpuUse, 'pf-icon-cpu') +
        summaryCard('Memory Usage', memUse, 'pf-icon-memory');

    // Render charts
    renderChart('chart-request-rate', series.requestRate, 'req/s', '#0066cc');
    renderChart('chart-latency', series.latency, 'ms', '#6a3d9a');
    renderChart('chart-gpu-util', series.gpuUtil, '%', '#e6550d');
    renderChart('chart-token-rate', series.tokenRate, 'tok/s', '#31a354');

    // Usage statistics table
    var tbody = document.getElementById('usage-stats-body');
    tbody.innerHTML = '';
    var stats = [
        ['Request Hit Rate', rps ? rps + ' req/s' : 'No data', 'Requests per second hitting this model endpoint'],
        ['Total Requests', totalReq || 'No data', 'Cumulative request count over the selected time window'],
        ['Active Requests', activeReq, 'Currently in-flight inference requests'],
        ['Average Latency (p50)', avgLat, 'Median end-to-end request latency'],
        ['P99 Latency', p99Lat, '99th percentile request latency'],
        ['Error Rate', errRate, 'Percentage of requests resulting in errors'],
        ['Token Throughput', tps ? tps + ' tok/s' : 'No data', 'Generated tokens per second (LLM models)'],
        ['Total Tokens Generated', totalTokens, 'Estimated total tokens generated over selected time window'],
        ['GPU Utilization', gpuUtil, 'Average GPU compute utilization (DCGM) across model pods'],
        ['KV Cache Usage', kvCache, 'GPU KV cache usage — indicates how much GPU memory is active for inference'],
        ['GPU Memory', gpuMem, 'GPU framebuffer memory used vs total'],
        ['CPU Usage', cpuUse, 'Container CPU consumption'],
        ['Memory Usage', memUse, 'Container working set memory'],
    ];
    stats.forEach(function (row) {
        var tr = document.createElement('tr');
        tr.innerHTML = '<td><strong>' + esc(row[0]) + '</strong></td><td>' + esc(row[1]) + '</td><td>' + esc(row[2]) + '</td>';
        tbody.appendChild(tr);
    });
}

function showMetricsError(msg) {
    document.getElementById('metrics-error-msg').textContent = msg;
    show('metrics-error');
}

// ========== SVG sparkline charts ==========

function renderChart(containerId, points, unit, color) {
    var el = document.getElementById(containerId);
    if (!points || points.length === 0) {
        el.innerHTML = '<div class="chart-no-data">No data available</div>';
        return;
    }

    var W = el.clientWidth || 500;
    var H = 210;
    var padL = 55, padR = 15, padT = 30, padB = 35;
    var cW = W - padL - padR;
    var cH = H - padT - padB;

    var vals = points.map(function (p) { return parseFloat(p.v) || 0; });
    var times = points.map(function (p) { return p.t; });
    var maxV = Math.max.apply(null, vals);
    if (maxV === 0) maxV = 1;
    // Add 10% headroom so peak values don't touch the top edge
    var chartMax = maxV * 1.1;
    var minT = times[0], maxT = times[times.length - 1];
    var rangeT = maxT - minT || 1;

    function x(i) { return padL + ((times[i] - minT) / rangeT) * cW; }
    function y(i) { return padT + cH - (vals[i] / chartMax) * cH; }

    // Build path
    var pathD = 'M ' + x(0) + ' ' + y(0);
    for (var i = 1; i < points.length; i++) {
        pathD += ' L ' + x(i) + ' ' + y(i);
    }

    // Fill area
    var areaD = pathD + ' L ' + x(points.length - 1) + ' ' + (padT + cH) + ' L ' + x(0) + ' ' + (padT + cH) + ' Z';

    // Y-axis labels (use actual maxV for labels, not chartMax)
    var yLabels = '';
    for (var j = 0; j <= 4; j++) {
        var yv = (maxV / 4) * j;
        var yy = padT + cH - (yv / chartMax) * cH;
        yLabels += '<text x="' + (padL - 6) + '" y="' + (yy + 4) + '" text-anchor="end" class="chart-label">' + fmtChartVal(yv) + '</text>';
        yLabels += '<line x1="' + padL + '" y1="' + yy + '" x2="' + (W - padR) + '" y2="' + yy + '" class="chart-grid"/>';
    }

    // X-axis labels (5 ticks)
    var xLabels = '';
    for (var k = 0; k <= 4; k++) {
        var xt = minT + (rangeT / 4) * k;
        var xx = padL + (k / 4) * cW;
        xLabels += '<text x="' + xx + '" y="' + (H - 5) + '" text-anchor="middle" class="chart-label">' + fmtTime(xt) + '</text>';
    }

    // Latest value annotation — position label below the point if near the top
    var lastIdx = points.length - 1;
    var lastVal = vals[lastIdx];
    var lastY = y(lastIdx);
    var labelY, labelAnchor;
    if (lastY < padT + 20) {
        // Point is near the top — place label below
        labelY = lastY + 18;
    } else {
        // Normal — place label above
        labelY = lastY - 8;
    }
    // Avoid label going off the right edge
    var labelX = x(lastIdx);
    if (labelX > W - padR - 60) {
        labelAnchor = 'end';
        labelX = labelX - 4;
    } else {
        labelAnchor = 'start';
        labelX = labelX + 6;
    }
    var lastLabel = '<text x="' + labelX + '" y="' + labelY + '" text-anchor="' + labelAnchor + '" class="chart-val-label" fill="' + color + '">' + fmtChartVal(lastVal) + ' ' + unit + '</text>';

    el.innerHTML =
        '<svg width="100%" height="' + H + '" viewBox="0 0 ' + W + ' ' + H + '" preserveAspectRatio="xMidYMid meet">' +
        yLabels + xLabels +
        '<path d="' + areaD + '" fill="' + color + '" opacity="0.1"/>' +
        '<path d="' + pathD + '" fill="none" stroke="' + color + '" stroke-width="2"/>' +
        '<circle cx="' + x(lastIdx) + '" cy="' + lastY + '" r="4" fill="' + color + '"/>' +
        lastLabel +
        '</svg>';
}

// ========== Analysis tab ==========

function loadAnalysisModelList() {
    fetch('/api/models/list')
        .then(handleJsonResponse)
        .then(function (models) {
            var sel = document.getElementById('analysis-model-select');
            var prev = sel.value;
            if (!analysisModelsLoaded) {
                sel.addEventListener('change', function () { if (this.value) refreshAnalysis(); });
            }
            analysisModelsLoaded = true;
            sel.innerHTML = '<option value="">-- Choose a model --</option>';
            (models || []).forEach(function (m) {
                var opt = document.createElement('option');
                opt.value = m.namespace + '/' + m.name;
                opt.textContent = m.namespace + ' / ' + m.name + (m.runtime ? ' (' + m.runtime + ')' : '');
                sel.appendChild(opt);
            });
            if (prev) sel.value = prev;
        })
        .catch(function (err) {
            document.getElementById('analysis-error-msg').textContent = 'Failed to load models: ' + err.message;
            show('analysis-error');
        });
}

function refreshAnalysis() {
    var sel = document.getElementById('analysis-model-select');
    if (!sel.value) return;
    var parts = sel.value.split('/');
    var namespace = parts[0], model = parts[1];

    hide('analysis-placeholder'); hide('analysis-content'); hide('analysis-error');
    show('analysis-loading');

    fetch('/api/analysis?model=' + encodeURIComponent(model) + '&namespace=' + encodeURIComponent(namespace))
        .then(handleJsonResponse)
        .then(function (data) {
            hide('analysis-loading');
            renderAnalysis(data);
            show('analysis-content');
        })
        .catch(function (err) {
            hide('analysis-loading');
            document.getElementById('analysis-error-msg').textContent = 'Failed to load analysis: ' + err.message;
            show('analysis-error');
        });
}

function renderAnalysis(data) {
    var perf = data.performance || {};
    var eff = data.efficiency || {};
    var res = data.resource || {};
    var cfg = data.config || {};

    // --- Performance ---
    var perfBody = document.getElementById('analysis-perf-body');
    var ttftP50 = fmtMs(perf.ttftP50Ms);
    var ttftP99 = fmtMs(perf.ttftP99Ms);
    var itlP50 = fmtMs(perf.itlP50Ms);
    var itlP99 = fmtMs(perf.itlP99Ms);
    var e2eP50 = fmtMs(perf.e2eLatencyP50Ms);
    var e2eP99 = fmtMs(perf.e2eLatencyP99Ms);
    var prefill = fmtMs(perf.prefillTimeMs);
    var decode = fmtMs(perf.decodeTimeMs);
    var queue = fmtMs(perf.queueWaitMs);
    var rps = perf.requestsPerSec ? fmtNum(perf.requestsPerSec, 2) + ' req/s' : '-';
    var tps = perf.tokensPerSec ? fmtNum(perf.tokensPerSec, 1) + ' tok/s' : '-';
    var avgPrompt = perf.avgPromptTokens ? fmtNum(perf.avgPromptTokens, 0) + ' tokens' : '-';
    var avgOutput = perf.avgOutputTokens ? fmtNum(perf.avgOutputTokens, 0) + ' tokens' : '-';

    var perfRows = [
        ['Time to First Token (p50)', ttftP50, 'Time until the first token is returned to the user', assessTTFT(perf.ttftP50Ms)],
        ['Time to First Token (p99)', ttftP99, '99th percentile — worst case TTFT', assessTTFT(perf.ttftP99Ms)],
        ['Inter-Token Latency (p50)', itlP50, 'Delay between consecutive generated tokens', assessITL(perf.itlP50Ms)],
        ['Inter-Token Latency (p99)', itlP99, '99th percentile ITL', assessITL(perf.itlP99Ms)],
        ['E2E Latency (p50)', e2eP50, 'Full request latency from arrival to completion', ''],
        ['E2E Latency (p99)', e2eP99, '99th percentile end-to-end latency', ''],
        ['Prefill Time (p50)', prefill, 'Time spent processing the input prompt', ''],
        ['Decode Time (p50)', decode, 'Time spent generating output tokens', ''],
        ['Queue Wait (p50)', queue, 'Time requests spend waiting in queue', assessQueue(perf.queueWaitMs)],
        ['Request Throughput', rps, 'Current requests processed per second', ''],
        ['Token Throughput', tps, 'Tokens generated per second', ''],
        ['Avg Prompt Size', avgPrompt, 'Average number of input tokens per request', ''],
        ['Avg Output Size', avgOutput, 'Average number of generated tokens per request', ''],
    ];
    perfBody.innerHTML = '';
    perfRows.forEach(function (row) {
        var tr = document.createElement('tr');
        tr.innerHTML = '<td><strong>' + esc(row[0]) + '</strong></td><td>' + esc(row[1]) + '</td><td>' + esc(row[2]) + '</td><td>' + row[3] + '</td>';
        perfBody.appendChild(tr);
    });

    // --- Efficiency ---
    var effBody = document.getElementById('analysis-eff-body');
    var cacheHitRate = eff.prefixCacheHitRate ? (parseFloat(eff.prefixCacheHitRate) * 100).toFixed(1) + '%' : '-';
    var kvCache = eff.kvCacheUsage ? (parseFloat(eff.kvCacheUsage) * 100).toFixed(1) + '%' : '-';
    var preemptions = eff.preemptionCount || '0';
    var running = (eff.requestsRunning !== '' && eff.requestsRunning != null) ? eff.requestsRunning : '-';
    var waiting = (eff.requestsWaiting !== '' && eff.requestsWaiting != null) ? eff.requestsWaiting : '-';
    var batchTokens = eff.avgBatchTokens ? fmtNum(eff.avgBatchTokens, 0) : '-';
    var tpg = eff.tokensPerGPU ? fmtNum(eff.tokensPerGPU, 1) + ' tok/s' : '-';

    var effRows = [
        ['Prefix Cache Hit Rate', cacheHitRate, 'How often cached prompt prefixes are reused', assessCacheHit(eff.prefixCacheHitRate)],
        ['Prefix Cache Hits / Queries', (eff.prefixCacheHits || '0') + ' / ' + (eff.prefixCacheQueries || '0'), 'Total cache hits vs total cache lookups', ''],
        ['KV Cache Usage', kvCache, 'GPU key-value cache memory in use — high values indicate memory pressure', assessKVCache(eff.kvCacheUsage)],
        ['Preemptions', preemptions, 'Requests evicted from GPU due to memory pressure', assessPreemptions(preemptions)],
        ['Requests Running', running, 'Currently executing inference requests', ''],
        ['Requests Waiting', waiting, 'Requests queued waiting for GPU capacity', assessWaiting(waiting)],
        ['Avg Batch Tokens', batchTokens, 'Average tokens processed per iteration batch', ''],
        ['Tokens per GPU', tpg, 'Token throughput per GPU — measures GPU efficiency', ''],
    ];
    effBody.innerHTML = '';
    effRows.forEach(function (row) {
        var tr = document.createElement('tr');
        tr.innerHTML = '<td><strong>' + esc(row[0]) + '</strong></td><td>' + esc(row[1]) + '</td><td>' + esc(row[2]) + '</td><td>' + row[3] + '</td>';
        effBody.appendChild(tr);
    });

    // --- Resource ---
    var resBody = document.getElementById('analysis-resource-body');
    var gpuUtil = res.gpuUtilization ? fmtNum(res.gpuUtilization, 1) + '%' : '-';
    var gpuMem = '-';
    if (res.gpuMemUsed && res.gpuMemTotal) {
        gpuMem = fmtBytes(res.gpuMemUsed) + ' / ' + fmtBytes(res.gpuMemTotal);
    }

    var cpuReq = res.cpuRequested ? fmtNum(res.cpuRequested, 1) + ' cores' : '-';
    var cpuLim = res.cpuLimit ? fmtNum(res.cpuLimit, 1) + ' cores' : '-';
    var cpuAct = res.cpuActual ? fmtNum(res.cpuActual, 2) + ' cores' : '-';
    var memReq = res.memRequested ? fmtMem(res.memRequested) : '-';
    var memLim = res.memLimit ? fmtMem(res.memLimit) : '-';
    var memAct = res.memActual ? fmtMem(res.memActual) : '-';

    var resRows = [
        ['CPU', cpuReq, cpuLim, cpuAct, assessResource(res.cpuActual, res.cpuRequested, res.cpuLimit)],
        ['Memory', memReq, memLim, memAct, assessResource(res.memActual, res.memRequested, res.memLimit)],
        ['GPU', res.gpusRequested || '-', '-', gpuUtil, ''],
        ['GPU Memory', '-', '-', gpuMem, ''],
    ];
    resBody.innerHTML = '';
    resRows.forEach(function (row) {
        var tr = document.createElement('tr');
        tr.innerHTML = '<td><strong>' + esc(row[0]) + '</strong></td><td>' + esc(row[1]) + '</td><td>' + esc(row[2]) + '</td><td>' + esc(row[3]) + '</td><td>' + row[4] + '</td>';
        resBody.appendChild(tr);
    });

    // --- Scheduling & Placement ---
    var sched = data.scheduling || {};
    var schedBody = document.getElementById('analysis-scheduling-body');
    var schedRows = [];
    var affinities = (sched.nodeAffinities || []).join(', ') || 'None';
    var selectors = (sched.nodeSelectors || []).join(', ') || 'None';
    var tolerations = (sched.tolerations || []).join(', ') || 'None';
    schedRows.push(['Node Affinities', affinities, 'Affinity rules constraining pod placement']);
    schedRows.push(['Node Selectors', selectors, 'Label selectors for node targeting']);
    schedRows.push(['Tolerations', tolerations, 'Tolerations allowing scheduling on tainted nodes']);
    schedRows.push(['Pod Spread', sched.podSpread || '-', sched.podSpreadDetail || 'Topology spread constraints for replicas']);
    schedBody.innerHTML = '';
    schedRows.forEach(function (row) {
        var tr = document.createElement('tr');
        tr.innerHTML = '<td><strong>' + esc(row[0]) + '</strong></td><td>' + esc(row[1]) + '</td><td>' + esc(row[2]) + '</td>';
        schedBody.appendChild(tr);
    });

    // GPU Fragmentation table
    var fragSection = document.getElementById('analysis-gpu-frag-section');
    var fragBody = document.getElementById('analysis-gpu-frag-body');
    var frags = sched.gpuFragmentation || [];
    if (frags.length > 0) {
        fragSection.style.display = '';
        fragBody.innerHTML = '';
        frags.forEach(function (f) {
            var tr = document.createElement('tr');
            var scoreColor = f.fragmentScore === 'High' ? 'red' : f.fragmentScore === 'Medium' ? 'orange' : 'green';
            tr.innerHTML = '<td>' + esc(f.nodeName) + '</td><td>' + esc(f.gpuProduct) + '</td><td>' + f.gpuUsed + '</td><td>' + f.gpuTotal + '</td><td>' + f.gpuFree + '</td><td>' + assessBadge(f.fragmentScore, scoreColor) + '</td>';
            fragBody.appendChild(tr);
        });
    } else {
        fragSection.style.display = 'none';
    }

    // --- Scaling & Capacity ---
    var scale = data.scaling || {};
    var scaleBody = document.getElementById('analysis-scaling-body');
    var satScore = scale.saturationScore || '-';
    var satAssess = '';
    var satNum = parseFloat(satScore);
    if (!isNaN(satNum)) {
        if (satNum < 30) satAssess = assessBadge('Low load', 'green');
        else if (satNum < 60) satAssess = assessBadge('Moderate', 'blue');
        else if (satNum < 80) satAssess = assessBadge('High', 'orange');
        else satAssess = assessBadge('Critical', 'red');
    }
    var scaleRows = [
        ['Saturation Score', satScore, scale.saturationDetail || 'Weighted score: KV cache 40%, GPU 40%, queue 20%', satAssess],
        ['Scaling Recommendation', scale.scalingRecommendation || '-', scale.scalingDetail || '', ''],
        ['Headroom — KV Cache', scale.headroomKVCache || '-', 'Remaining KV cache capacity', ''],
        ['Headroom — GPU', scale.headroomGPU || '-', 'Remaining GPU utilization headroom', ''],
        ['Headroom — Queue', scale.headroomQueue || '-', 'Queue depth headroom', ''],
        ['Headroom — Overall', scale.headroomOverall || '-', 'Combined headroom assessment', assessHeadroom(scale.headroomOverall)],
    ];
    scaleBody.innerHTML = '';
    scaleRows.forEach(function (row) {
        var tr = document.createElement('tr');
        tr.innerHTML = '<td><strong>' + esc(row[0]) + '</strong></td><td>' + esc(row[1]) + '</td><td>' + esc(row[2]) + '</td><td>' + row[3] + '</td>';
        scaleBody.appendChild(tr);
    });

    // --- Config Audit ---
    var audit = data.configAudit || {};
    var auditBody = document.getElementById('analysis-configaudit-body');
    var wasteAssess = '';
    if (audit.contextLenWaste) {
        var wasteNum = parseFloat(audit.contextLenWaste);
        if (!isNaN(wasteNum)) {
            if (wasteNum > 80) wasteAssess = assessBadge('Significant waste — reduce max_model_len', 'orange');
            else if (wasteNum > 50) wasteAssess = assessBadge('Moderate waste', 'blue');
            else wasteAssess = assessBadge('Well utilized', 'green');
        }
    }
    var auditRows = [
        ['Tensor Parallelism', audit.tensorParallelism || '-', audit.tpDetail || '', ''],
        ['Context Length (configured)', audit.contextLenConfig || '-', 'max_model_len setting', ''],
        ['Context Length (actual peak)', audit.contextLenActual || '-', 'Peak context length seen in use', ''],
        ['Context Length Waste', audit.contextLenWaste || '-', audit.contextLenDetail || 'Unused portion of configured context window', wasteAssess],
        ['Quantization', audit.quantization || '-', audit.quantizationDetail || '', ''],
        ['Runtime Version', audit.runtimeVersion || '-', audit.runtimeVersionDetail || '', ''],
    ];
    auditBody.innerHTML = '';
    auditRows.forEach(function (row) {
        var tr = document.createElement('tr');
        tr.innerHTML = '<td><strong>' + esc(row[0]) + '</strong></td><td>' + esc(row[1]) + '</td><td>' + esc(row[2]) + '</td><td>' + row[3] + '</td>';
        auditBody.appendChild(tr);
    });

    // --- Network & Connectivity ---
    var net = data.network || {};
    var netBody = document.getElementById('analysis-network-body');
    var policyCount = net.policiesAffecting || 0;
    var policyDetails = (net.policyDetails || []).join(', ') || 'None';
    var reachAssess = net.inferenceReachable || '-';
    var netRows = [
        ['Network Policies', policyCount + ' affecting', policyDetails],
        ['Inference URL', net.inferenceURL || '-', 'Reachability: ' + reachAssess],
    ];
    netBody.innerHTML = '';
    netRows.forEach(function (row) {
        var tr = document.createElement('tr');
        tr.innerHTML = '<td><strong>' + esc(row[0]) + '</strong></td><td>' + esc(row[1]) + '</td><td>' + esc(row[2]) + '</td>';
        netBody.appendChild(tr);
    });

    // --- Health & Reliability ---
    var health = data.health || {};
    var healthBody = document.getElementById('analysis-health-body');
    var errRateAssess = '';
    if (health.errorRate5m) {
        var errVal = parseFloat(health.errorRate5m);
        if (!isNaN(errVal)) {
            if (errVal === 0) errRateAssess = assessBadge('No errors', 'green');
            else if (errVal < 1) errRateAssess = assessBadge('Low', 'blue');
            else if (errVal < 5) errRateAssess = assessBadge('Moderate', 'orange');
            else errRateAssess = assessBadge('High', 'red');
        }
    }
    var uptimeAssess = '';
    if (health.uptimePercent) {
        var upVal = parseFloat(health.uptimePercent);
        if (!isNaN(upVal)) {
            if (upVal >= 99.9) uptimeAssess = assessBadge('Excellent', 'green');
            else if (upVal >= 99) uptimeAssess = assessBadge('Good', 'green');
            else if (upVal >= 95) uptimeAssess = assessBadge('Fair', 'orange');
            else uptimeAssess = assessBadge('Poor', 'red');
        }
    }
    var healthRows = [
        ['Error Rate (5m)', health.errorRate5m || '-', 'Error rate over last 5 minutes', errRateAssess],
        ['Error Rate (total)', health.errorRateTotal || '-', 'Overall error rate', ''],
        ['Uptime', health.uptimePercent || '-', health.uptimeDetail || '', uptimeAssess],
        ['Ready Since', health.readySince || '-', 'When the model last became ready', ''],
    ];
    // Error categories
    var errCats = health.errorCategories || [];
    errCats.forEach(function (cat) {
        healthRows.push([cat.category, cat.count || '0', cat.detail || '', '']);
    });
    healthBody.innerHTML = '';
    healthRows.forEach(function (row) {
        var tr = document.createElement('tr');
        tr.innerHTML = '<td><strong>' + esc(row[0]) + '</strong></td><td>' + esc(row[1]) + '</td><td>' + esc(row[2]) + '</td><td>' + row[3] + '</td>';
        healthBody.appendChild(tr);
    });

    // --- Cost & Efficiency ---
    var cost = data.cost || {};
    var costBody = document.getElementById('analysis-cost-body');
    var costEffAssess = '';
    if (cost.costEfficiencyScore) {
        var ceVal = cost.costEfficiencyScore.toLowerCase();
        if (ceVal === 'high') costEffAssess = assessBadge('High efficiency', 'green');
        else if (ceVal === 'medium') costEffAssess = assessBadge('Moderate', 'blue');
        else if (ceVal === 'low') costEffAssess = assessBadge('Low — consider optimizing', 'orange');
        else costEffAssess = assessBadge(cost.costEfficiencyScore, 'blue');
    }
    var opAssess = '';
    if (cost.overProvisionScore) {
        var opVal = cost.overProvisionScore.toLowerCase();
        if (opVal === 'none' || opVal === 'low') opAssess = assessBadge('Well-sized', 'green');
        else if (opVal === 'moderate') opAssess = assessBadge('Some waste', 'orange');
        else if (opVal === 'high') opAssess = assessBadge('Significant waste', 'red');
        else opAssess = assessBadge(cost.overProvisionScore, 'blue');
    }
    var costRows = [
        ['Tokens / GPU-hour', cost.tokensPerGPUHour || '-', 'Token throughput normalized per GPU per hour', costEffAssess],
        ['Cost Efficiency', cost.costEfficiencyScore || '-', 'Overall GPU cost efficiency rating', ''],
        ['Over-Provisioning (CPU)', cost.overProvisionCPU || '-', 'CPU allocation vs actual usage ratio', ''],
        ['Over-Provisioning (Memory)', cost.overProvisionMem || '-', 'Memory allocation vs actual usage ratio', ''],
        ['Over-Provisioning Score', cost.overProvisionScore || '-', cost.overProvisionDetail || '', opAssess],
        ['Right-size CPU', cost.rightsizeCPU || '-', 'Recommended CPU request based on usage + 30% headroom', ''],
        ['Right-size Memory', cost.rightsizeMem || '-', 'Recommended memory request based on usage + 30% headroom', ''],
    ];
    if (cost.rightsizeDetail) {
        costRows.push(['Right-sizing Note', '', cost.rightsizeDetail, '']);
    }
    costBody.innerHTML = '';
    costRows.forEach(function (row) {
        var tr = document.createElement('tr');
        tr.innerHTML = '<td><strong>' + esc(row[0]) + '</strong></td><td>' + esc(row[1]) + '</td><td>' + esc(row[2]) + '</td><td>' + row[3] + '</td>';
        costBody.appendChild(tr);
    });
}

// --- Assessment helpers ---

function assessBadge(text, color) {
    return '<span class="pf-v5-c-label pf-m-' + color + '"><span class="pf-v5-c-label__content">' + esc(text) + '</span></span>';
}

function assessTTFT(ms) {
    if (!ms) return '';
    var v = parseFloat(ms);
    if (isNaN(v)) return '';
    if (v < 200) return assessBadge('Excellent', 'green');
    if (v < 500) return assessBadge('Good', 'green');
    if (v < 1000) return assessBadge('Acceptable', 'blue');
    if (v < 3000) return assessBadge('Slow', 'orange');
    return assessBadge('Very Slow', 'red');
}

function assessITL(ms) {
    if (!ms) return '';
    var v = parseFloat(ms);
    if (isNaN(v)) return '';
    if (v < 30) return assessBadge('Excellent', 'green');
    if (v < 60) return assessBadge('Good', 'green');
    if (v < 100) return assessBadge('Acceptable', 'blue');
    if (v < 200) return assessBadge('Slow', 'orange');
    return assessBadge('Very Slow', 'red');
}

function assessQueue(ms) {
    if (!ms) return '';
    var v = parseFloat(ms);
    if (isNaN(v)) return '';
    if (v < 10) return assessBadge('No queuing', 'green');
    if (v < 100) return assessBadge('Light queuing', 'blue');
    if (v < 500) return assessBadge('Moderate queuing', 'orange');
    return assessBadge('Heavy queuing — scale up', 'red');
}

function assessCacheHit(rate) {
    if (!rate) return '';
    var v = parseFloat(rate);
    if (isNaN(v)) return '';
    if (v > 0.8) return assessBadge('Excellent cache reuse', 'green');
    if (v > 0.5) return assessBadge('Good', 'green');
    if (v > 0.2) return assessBadge('Low reuse', 'blue');
    return assessBadge('Minimal caching benefit', 'orange');
}

function assessKVCache(usage) {
    if (!usage) return '';
    var v = parseFloat(usage);
    if (isNaN(v)) return '';
    if (v < 0.5) return assessBadge('Healthy', 'green');
    if (v < 0.8) return assessBadge('Moderate', 'blue');
    if (v < 0.95) return assessBadge('High — watch for preemptions', 'orange');
    return assessBadge('Critical — likely causing preemptions', 'red');
}

function assessPreemptions(count) {
    var v = parseInt(count);
    if (isNaN(v) || v === 0) return assessBadge('None', 'green');
    if (v < 10) return assessBadge('Low', 'blue');
    if (v < 100) return assessBadge('Moderate — consider more GPU memory', 'orange');
    return assessBadge('High — increase GPU memory or reduce load', 'red');
}

function assessWaiting(count) {
    if (!count || count === '-') return '';
    var v = parseInt(count);
    if (isNaN(v) || v === 0) return assessBadge('No queue', 'green');
    if (v < 5) return assessBadge('Light queue', 'blue');
    if (v < 20) return assessBadge('Moderate — consider scaling', 'orange');
    return assessBadge('Heavy queue — scale up', 'red');
}

function assessHeadroom(overall) {
    if (!overall) return '';
    var v = overall.toLowerCase();
    if (v.indexOf('high') !== -1 || v.indexOf('plenty') !== -1) return assessBadge('Plenty of room', 'green');
    if (v.indexOf('moderate') !== -1 || v.indexOf('adequate') !== -1) return assessBadge('Adequate', 'blue');
    if (v.indexOf('low') !== -1) return assessBadge('Low headroom', 'orange');
    if (v.indexOf('critical') !== -1 || v.indexOf('none') !== -1) return assessBadge('Critical', 'red');
    return assessBadge(overall, 'blue');
}

function assessResource(actual, requested, limit) {
    if (!actual) return '';
    var act = parseFloat(actual);
    if (isNaN(act)) return '';
    // Compare against limit if available, otherwise requested
    var cap = 0;
    if (limit) cap = parseFloat(limit);
    if (isNaN(cap) || cap === 0) {
        if (requested) cap = parseFloat(requested);
    }
    if (isNaN(cap) || cap === 0) return '';
    var ratio = act / cap;
    if (ratio < 0.3) return assessBadge('Low utilization', 'blue');
    if (ratio < 0.7) return assessBadge('Well-sized', 'green');
    if (ratio < 0.9) return assessBadge('Good utilization', 'green');
    return assessBadge('Near limit — consider increasing', 'orange');
}

function fmtMs(v) {
    if (!v) return '-';
    var n = parseFloat(v);
    if (isNaN(n)) return '-';
    if (n < 1) return n.toFixed(2) + ' ms';
    if (n < 10) return n.toFixed(1) + ' ms';
    if (n < 1000) return Math.round(n) + ' ms';
    return (n / 1000).toFixed(2) + ' s';
}

// ========== Firewalls tab ==========

function loadFirewalls() {
    show('firewalls-loading'); hide('firewalls-empty'); hide('firewalls-content');
    fetch('/api/network-policies')
        .then(handleJsonResponse)
        .then(function (policies) {
            hide('firewalls-loading');
            if (!policies || policies.length === 0) { show('firewalls-empty'); return; }
            renderFirewalls(policies); show('firewalls-content');
        })
        .catch(function (err) {
            hide('firewalls-loading');
            document.getElementById('firewalls-content').innerHTML = '<div class="pf-v5-c-alert pf-m-danger"><h4 class="pf-v5-c-alert__title">' + esc(err.message) + '</h4></div>';
            show('firewalls-content');
        });
}

function renderFirewalls(policies) {
    var container = document.getElementById('firewalls-content');
    var byNS = {};
    policies.forEach(function (p) { if (!byNS[p.namespace]) byNS[p.namespace] = []; byNS[p.namespace].push(p); });
    var html = '';
    Object.keys(byNS).sort().forEach(function (ns) {
        html += '<div class="pf-v5-c-card" style="margin-bottom:16px;">';
        html += '<div class="pf-v5-c-card__header"><div class="pf-v5-c-card__title"><h2 class="pf-v5-c-card__title-text">Namespace: ' + esc(ns) + '</h2></div></div>';
        html += '<div class="pf-v5-c-card__body" style="padding:0;"><table class="pf-v5-c-table pf-m-grid-md"><thead><tr><th>Policy Name</th><th>Target</th><th>Types</th><th>Rules</th></tr></thead><tbody>';
        byNS[ns].forEach(function (p) {
            html += '<tr><td><strong>' + esc(p.name) + '</strong></td><td>' + esc(p.target) + '</td>';
            html += '<td>' + (p.types || []).map(function (t) {
                return '<span class="pf-v5-c-label pf-m-' + (t === 'Ingress' ? 'blue' : 'green') + '" style="margin:1px;"><span class="pf-v5-c-label__content">' + esc(t) + '</span></span>';
            }).join(' ') + '</td>';
            html += '<td><ul class="firewall-rules">';
            (p.rules || []).forEach(function (rule) {
                html += '<li class="' + (rule.indexOf('DENY') === 0 ? 'rule-deny' : 'rule-allow') + '">' + esc(rule) + '</li>';
            });
            html += '</ul></td></tr>';
        });
        html += '</tbody></table></div></div>';
    });
    container.innerHTML = html;
}

// ========== Live tab ==========

var liveInterval = null;
var liveHistory = { running: [], waiting: [], kv: [], genTokRate: [], promptTokRate: [], reqRate: [] };
var liveMaxHistory = 0; // 0 = unlimited — keep all captured data
var livePrevCounters = null;
var livePrevTimestamp = null;
var liveRateAvgs = {}; // accumulate rates for averaging
var liveRateWindow = {}; // rolling window for smoothed rates (last 10 samples)

function loadLiveModelList() {
    fetch('/api/models/list')
        .then(handleJsonResponse)
        .then(function (models) {
            var sel = document.getElementById('live-model-select');
            var prev = sel.value;
            if (!liveModelsLoaded) {
                sel.addEventListener('change', function () {
                    if (this.value) {
                        hide('live-placeholder');
                        show('loadtest-controls');
                        show('livestats-section');
                    } else {
                        show('live-placeholder');
                        hide('loadtest-controls');
                        hide('livestats-section');
                        hide('live-content');
                        stopLive();
                        stopLoadTestPolling();
                    }
                });
            }
            liveModelsLoaded = true;
            sel.innerHTML = '<option value="">-- Choose a model --</option>';
            (models || []).forEach(function (m) {
                var opt = document.createElement('option');
                opt.value = m.namespace + '/' + m.name;
                opt.textContent = m.namespace + ' / ' + m.name + (m.runtime ? ' (' + m.runtime + ')' : '');
                sel.appendChild(opt);
            });
            if (prev) sel.value = prev;
        })
        .catch(function (err) {
            document.getElementById('live-error-msg').textContent = 'Failed to load models: ' + err.message;
            show('live-error');
        });
}

function toggleLive() {
    if (liveInterval) {
        stopLive();
    } else {
        startLive();
    }
}

function startLive() {
    var sel = document.getElementById('live-model-select');
    if (!sel.value) {
        document.getElementById('live-error-msg').textContent = 'Please select a model first.';
        show('live-error');
        return;
    }
    hide('live-error');

    // Reset history
    liveHistory = { running: [], waiting: [], kv: [], genTokRate: [], promptTokRate: [], reqRate: [] };
    livePrevCounters = null;
    livePrevTimestamp = null;
    liveRateAvgs = {};
    liveRateWindow = {};

    document.getElementById('live-toggle-btn').textContent = 'Stop';
    document.getElementById('live-status-indicator').className = 'live-indicator-on';
    document.getElementById('live-status-text').textContent = 'Live — refreshing every 2s';

    hide('live-placeholder');
    show('live-content');

    fetchLive();
    liveInterval = setInterval(fetchLive, 2000);
}

function stopLive() {
    if (liveInterval) {
        clearInterval(liveInterval);
        liveInterval = null;
    }
    document.getElementById('live-toggle-btn').textContent = 'Start';
    document.getElementById('live-status-indicator').className = 'live-indicator-off';
    document.getElementById('live-status-text').textContent = 'Stopped';
}

function fetchLive() {
    var sel = document.getElementById('live-model-select');
    if (!sel.value) { stopLive(); return; }
    var parts = sel.value.split('/');
    var namespace = parts[0], model = parts[1];

    fetch('/api/live?model=' + encodeURIComponent(model) + '&namespace=' + encodeURIComponent(namespace))
        .then(handleJsonResponse)
        .then(function (data) {
            hide('live-error');
            renderLive(data);
        })
        .catch(function (err) {
            document.getElementById('live-error-msg').textContent = 'Scrape failed: ' + err.message;
            show('live-error');
        });
}

function renderLive(data) {
    var g = data.gauges || {};
    var c = data.counters || {};
    var ts = data.timestamp;

    // Compute instant rates from counter deltas
    var rates = {};
    if (livePrevCounters && livePrevTimestamp && ts > livePrevTimestamp) {
        var dt = ts - livePrevTimestamp;
        for (var key in c) {
            if (livePrevCounters[key] !== undefined) {
                rates[key] = (c[key] - livePrevCounters[key]) / dt;
            }
        }
    }
    livePrevCounters = c;
    livePrevTimestamp = ts;

    // Accumulate rates for overall and rolling averages
    for (var rk in rates) {
        if (!liveRateAvgs[rk]) liveRateAvgs[rk] = { sum: 0, count: 0 };
        liveRateAvgs[rk].sum += rates[rk];
        liveRateAvgs[rk].count += 1;
        // Rolling window (last 10 samples) for smoothed display
        if (!liveRateWindow[rk]) liveRateWindow[rk] = [];
        liveRateWindow[rk].push(rates[rk]);
        if (liveRateWindow[rk].length > 10) liveRateWindow[rk].shift();
    }
    function avgRate(key) {
        var a = liveRateAvgs[key];
        if (!a || a.count === 0) return 0;
        return a.sum / a.count;
    }
    function smoothRate(key) {
        var w = liveRateWindow[key];
        if (!w || w.length === 0) return 0;
        var s = 0;
        for (var i = 0; i < w.length; i++) s += w[i];
        return s / w.length;
    }

    // Key values
    var running = g['vllm:num_requests_running'] || 0;
    var waiting = g['vllm:num_requests_waiting'] || 0;
    var swapped = g['vllm:num_requests_swapped'] || 0;
    var kvUsage = (g['vllm:kv_cache_usage_perc'] || 0) * 100;
    var preemptions = g['vllm:num_preemptions_total'] || c['vllm:num_preemptions_total'] || 0;
    var processMem = g['process_resident_memory_bytes'] || 0;

    var totalSuccess = c['vllm:request_success_total'] || 0;
    var totalGenTokens = c['vllm:generation_tokens_total'] || 0;
    var totalPromptTokens = c['vllm:prompt_tokens_total'] || 0;
    var cacheHits = c['vllm:prefix_cache_hits_total'] || 0;
    var cacheQueries = c['vllm:prefix_cache_queries_total'] || 0;

    // Smoothed rates (rolling 10-sample window) — avoids 0-spike flickering
    var reqRate = smoothRate('vllm:request_success_total');
    var genTokRate = smoothRate('vllm:generation_tokens_total');
    var promptTokRate = smoothRate('vllm:prompt_tokens_total');

    // Overall average rates (since start)
    var avgReqRate = avgRate('vllm:request_success_total');
    var avgGenTokRate = avgRate('vllm:generation_tokens_total');
    var avgPromptTokRate = avgRate('vllm:prompt_tokens_total');

    var cacheHitRate = cacheQueries > 0 ? ((cacheHits / cacheQueries) * 100) : 0;

    // Update history
    pushHistory('running', ts, running);
    pushHistory('waiting', ts, waiting);
    pushHistory('kv', ts, kvUsage);
    pushHistory('genTokRate', ts, genTokRate);
    pushHistory('promptTokRate', ts, promptTokRate);
    pushHistory('reqRate', ts, reqRate);

    // Gauges — only show what's available
    var gaugeCards = '';
    if (g['vllm:num_requests_running'] !== undefined) gaugeCards += liveSummaryCard('Requests Running', running.toFixed(0), '#0066cc');
    if (g['vllm:num_requests_waiting'] !== undefined) gaugeCards += liveSummaryCard('Requests Waiting', waiting.toFixed(0), waiting > 0 ? '#f0ab00' : '#3e8635');
    if (g['vllm:num_requests_swapped'] !== undefined && swapped > 0) gaugeCards += liveSummaryCard('Requests Swapped', swapped.toFixed(0), '#c9190b');
    if (g['vllm:kv_cache_usage_perc'] !== undefined) gaugeCards += liveSummaryCard('KV Cache', kvUsage.toFixed(1) + '%', kvUsage > 90 ? '#c9190b' : kvUsage > 70 ? '#f0ab00' : '#3e8635');
    if (c['vllm:generation_tokens_total'] !== undefined) gaugeCards += liveSummaryCard('Gen tok/s', genTokRate.toFixed(1) + ' (avg ' + avgGenTokRate.toFixed(1) + ')', '#31a354');
    if (c['vllm:prompt_tokens_total'] !== undefined) gaugeCards += liveSummaryCard('Prompt tok/s', promptTokRate.toFixed(1) + ' (avg ' + avgPromptTokRate.toFixed(1) + ')', '#6a3d9a');
    if (c['vllm:request_success_total'] !== undefined) gaugeCards += liveSummaryCard('Req/s', reqRate.toFixed(2) + ' (avg ' + avgReqRate.toFixed(2) + ')', '#0066cc');
    if (c['vllm:request_success_total'] !== undefined) gaugeCards += liveSummaryCard('Total Requests', fmtLargeNum(totalSuccess), '#0066cc');
    // GPU blocks (if exposed by this vLLM version)
    if (g['vllm:num_gpu_blocks_total'] !== undefined) {
        var gpuBlocksUsed = g['vllm:num_gpu_blocks_used'] || 0;
        var gpuBlocksTotal = g['vllm:num_gpu_blocks_total'] || 0;
        gaugeCards += liveSummaryCard('GPU Blocks', gpuBlocksUsed.toFixed(0) + ' / ' + gpuBlocksTotal.toFixed(0), '#0066cc');
    }
    // Throughput gauges (if exposed by newer vLLM)
    if (g['vllm:avg_generation_throughput_toks_per_s'] !== undefined) gaugeCards += liveSummaryCard('Gen Throughput', g['vllm:avg_generation_throughput_toks_per_s'].toFixed(1) + ' tok/s', '#31a354');
    if (g['vllm:avg_prompt_throughput_toks_per_s'] !== undefined) gaugeCards += liveSummaryCard('Prompt Throughput', g['vllm:avg_prompt_throughput_toks_per_s'].toFixed(1) + ' tok/s', '#6a3d9a');
    if (g['vllm:num_preemptions_total'] !== undefined || c['vllm:num_preemptions_total'] !== undefined) gaugeCards += liveSummaryCard('Preemptions', preemptions.toFixed(0), preemptions > 0 ? '#c9190b' : '#3e8635');
    if (cacheQueries > 0) gaugeCards += liveSummaryCard('Cache Hit Rate', cacheHitRate.toFixed(1) + '%', cacheHitRate > 50 ? '#3e8635' : '#6a6e73');
    if (processMem > 0) gaugeCards += liveSummaryCard('Process Memory', fmtBytes(processMem), '#6a6e73');
    document.getElementById('live-gauges').innerHTML = gaugeCards;

    // Charts
    renderLiveChart('live-chart-requests', [
        { points: liveHistory.running, label: 'Running', color: '#0066cc' },
        { points: liveHistory.waiting, label: 'Waiting', color: '#f0ab00' }
    ]);
    renderLiveChart('live-chart-kv', [
        { points: liveHistory.kv, label: 'KV Cache %', color: '#e6550d' }
    ]);
    renderLiveChart('live-chart-throughput', [
        { points: liveHistory.genTokRate, label: 'Gen tok/s', color: '#31a354' },
        { points: liveHistory.promptTokRate, label: 'Prompt tok/s', color: '#6a3d9a' }
    ]);
    renderLiveChart('live-chart-completed', [
        { points: liveHistory.reqRate, label: 'Req/s', color: '#0066cc' }
    ]);

    // Counters table — only show available metrics
    var tbody = document.getElementById('live-counters-body');
    var counterRows = [];
    if (c['vllm:request_success_total'] !== undefined) counterRows.push(['vllm:request_success_total', 'Successful Requests', totalSuccess, rates['vllm:request_success_total'], avgReqRate]);
    if (c['vllm:generation_tokens_total'] !== undefined) counterRows.push(['vllm:generation_tokens_total', 'Generated Tokens', totalGenTokens, rates['vllm:generation_tokens_total'], avgGenTokRate]);
    if (c['vllm:prompt_tokens_total'] !== undefined) counterRows.push(['vllm:prompt_tokens_total', 'Prompt Tokens', totalPromptTokens, rates['vllm:prompt_tokens_total'], avgPromptTokRate]);
    if (c['vllm:prefix_cache_hits_total'] !== undefined) counterRows.push(['vllm:prefix_cache_hits_total', 'Prefix Cache Hits', cacheHits, rates['vllm:prefix_cache_hits_total'], avgRate('vllm:prefix_cache_hits_total')]);
    if (c['vllm:prefix_cache_queries_total'] !== undefined) counterRows.push(['vllm:prefix_cache_queries_total', 'Prefix Cache Queries', cacheQueries, rates['vllm:prefix_cache_queries_total'], avgRate('vllm:prefix_cache_queries_total')]);

    // Histogram averages (lifetime)
    var e2eSum = c['vllm:e2e_request_latency_seconds_sum'];
    var e2eCount = c['vllm:e2e_request_latency_seconds_count'];
    if (e2eSum !== undefined && e2eCount !== undefined && e2eCount > 0) {
        counterRows.push(['avg_e2e', 'Avg E2E Latency', (e2eSum / e2eCount * 1000).toFixed(1) + ' ms', null, null]);
    }
    var ttftSum = c['vllm:time_to_first_token_seconds_sum'];
    var ttftCount = c['vllm:time_to_first_token_seconds_count'];
    if (ttftSum !== undefined && ttftCount !== undefined && ttftCount > 0) {
        counterRows.push(['avg_ttft', 'Avg Time to First Token', (ttftSum / ttftCount * 1000).toFixed(1) + ' ms', null, null]);
    }
    var itlSum = c['vllm:inter_token_latency_seconds_sum'];
    var itlCount = c['vllm:inter_token_latency_seconds_count'];
    if (itlSum !== undefined && itlCount !== undefined && itlCount > 0) {
        counterRows.push(['avg_itl', 'Avg Inter-Token Latency', (itlSum / itlCount * 1000).toFixed(1) + ' ms', null, null]);
    }

    tbody.innerHTML = '';
    counterRows.forEach(function (row) {
        var tr = document.createElement('tr');
        var val = typeof row[2] === 'number' ? fmtLargeNum(row[2]) : row[2];
        var rateStr = row[3] != null ? fmtNum(row[3], 2) + '/s' : '-';
        if (row[4] != null) rateStr += ' (avg ' + fmtNum(row[4], 2) + '/s)';
        tr.innerHTML = '<td><strong>' + esc(row[1]) + '</strong></td><td>' + val + '</td><td>' + rateStr + '</td><td><code>' + esc(row[0]) + '</code></td>';
        tbody.appendChild(tr);
    });
}

function pushHistory(key, ts, val) {
    liveHistory[key].push({ t: ts, v: val });
}

function liveSummaryCard(title, value, color) {
    return '<div class="pf-v5-c-card summary-card live-gauge-card"><div class="pf-v5-c-card__body summary-card-body">' +
        '<div class="summary-card-text"><div class="summary-card-value" style="color:' + color + ';">' + value + '</div>' +
        '<div class="summary-card-label">' + esc(title) + '</div></div></div></div>';
}

function renderLiveChart(containerId, seriesList) {
    var el = document.getElementById(containerId);
    if (!seriesList || seriesList.length === 0 || seriesList[0].points.length < 2) {
        el.innerHTML = '<div class="chart-no-data">Collecting data...</div>';
        return;
    }

    // Apply time window filter
    var windowSec = parseInt(document.getElementById('live-chart-window').value) || 0;
    var now = Math.floor(Date.now() / 1000);
    var filteredSeries = seriesList.map(function (s) {
        var pts = s.points;
        if (windowSec > 0) {
            var cutoff = now - windowSec;
            pts = pts.filter(function (p) { return p.t >= cutoff; });
        }
        return { points: pts, label: s.label, color: s.color };
    });

    // Update data points counter (once, from first chart call per render)
    var dpEl = document.getElementById('live-data-points');
    if (dpEl && seriesList[0].points.length > 0) {
        dpEl.textContent = seriesList[0].points.length + ' data points captured';
    }

    if (filteredSeries[0].points.length < 2) {
        el.innerHTML = '<div class="chart-no-data">Collecting data...</div>';
        return;
    }

    var W = el.clientWidth || 500;
    var H = 210;
    var padL = 55, padR = 15, padT = 30, padB = 35;
    var cW = W - padL - padR;
    var cH = H - padT - padB;

    // Compute global min/max time and value
    var minT = Infinity, maxT = -Infinity, maxV = 0;
    filteredSeries.forEach(function (s) {
        s.points.forEach(function (p) {
            if (p.t < minT) minT = p.t;
            if (p.t > maxT) maxT = p.t;
            if (p.v > maxV) maxV = p.v;
        });
    });
    if (maxV === 0) maxV = 1;
    var chartMax = maxV * 1.1;
    var rangeT = maxT - minT || 1;

    function xPos(t) { return padL + ((t - minT) / rangeT) * cW; }
    function yPos(v) { return padT + cH - (v / chartMax) * cH; }

    // Y-axis labels
    var yLabels = '';
    for (var j = 0; j <= 4; j++) {
        var yv = (maxV / 4) * j;
        var yy = yPos(yv);
        yLabels += '<text x="' + (padL - 6) + '" y="' + (yy + 4) + '" text-anchor="end" class="chart-label">' + fmtChartVal(yv) + '</text>';
        yLabels += '<line x1="' + padL + '" y1="' + yy + '" x2="' + (W - padR) + '" y2="' + yy + '" class="chart-grid"/>';
    }

    // X-axis labels
    var xLabels = '';
    for (var k = 0; k <= 4; k++) {
        var xt = minT + (rangeT / 4) * k;
        var xx = padL + (k / 4) * cW;
        xLabels += '<text x="' + xx + '" y="' + (H - 5) + '" text-anchor="middle" class="chart-label">' + fmtTime(xt) + '</text>';
    }

    // Draw series
    var paths = '';
    var legends = '';
    var legendX = padL + 5;
    filteredSeries.forEach(function (s, si) {
        if (s.points.length < 2) return;
        var d = 'M ' + xPos(s.points[0].t) + ' ' + yPos(s.points[0].v);
        for (var i = 1; i < s.points.length; i++) {
            d += ' L ' + xPos(s.points[i].t) + ' ' + yPos(s.points[i].v);
        }
        // Area fill
        var areaD = d + ' L ' + xPos(s.points[s.points.length - 1].t) + ' ' + (padT + cH) + ' L ' + xPos(s.points[0].t) + ' ' + (padT + cH) + ' Z';
        paths += '<path d="' + areaD + '" fill="' + s.color + '" opacity="0.08"/>';
        paths += '<path d="' + d + '" fill="none" stroke="' + s.color + '" stroke-width="2"/>';

        // Latest value dot + label
        var lastP = s.points[s.points.length - 1];
        var lx = xPos(lastP.t), ly = yPos(lastP.v);
        paths += '<circle cx="' + lx + '" cy="' + ly + '" r="3" fill="' + s.color + '"/>';

        // Legend
        legends += '<rect x="' + legendX + '" y="' + (padT - 18) + '" width="12" height="3" fill="' + s.color + '"/>';
        legends += '<text x="' + (legendX + 16) + '" y="' + (padT - 14) + '" class="chart-label" fill="' + s.color + '">' + s.label + ': ' + fmtChartVal(lastP.v) + '</text>';
        legendX += 100 + s.label.length * 4;
    });

    el.innerHTML =
        '<svg width="100%" height="' + H + '" viewBox="0 0 ' + W + ' ' + H + '" preserveAspectRatio="xMidYMid meet">' +
        yLabels + xLabels + paths + legends +
        '</svg>';
}

// ========== Load test ==========

function fetchLoadTestBanner() {
    fetch('/api/loadtest/status')
    .then(handleJsonResponse)
    .then(function (data) { updateLoadTestBanner(data); })
    .catch(function () {});
}

function confirmStartLoadTest() {
    var sel = document.getElementById('live-model-select');
    if (!sel.value) {
        document.getElementById('live-error-msg').textContent = 'Please select a model first.';
        show('live-error');
        return;
    }

    var existing = document.getElementById('loadtest-start-dialog');
    if (existing) existing.remove();

    var backdrop = document.createElement('div');
    backdrop.id = 'loadtest-start-dialog';
    backdrop.className = 'pf-v5-c-backdrop';
    backdrop.innerHTML =
        '<div class="pf-v5-c-modal-box pf-m-sm" role="dialog" aria-modal="true" style="position:fixed;top:50%;left:50%;transform:translate(-50%,-50%);z-index:10001;max-width:480px;">' +
        '  <div class="pf-v5-c-modal-box__header">' +
        '    <h1 class="pf-v5-c-modal-box__title">Start Load Test</h1>' +
        '  </div>' +
        '  <div class="pf-v5-c-modal-box__body">' +
        '    <p>Would you also like to enable <strong>live metric watching</strong> for this model? This shows real-time pod metrics (GPU usage, KV cache, request rates) while the test runs.</p>' +
        '    <div style="margin-top:12px;">' +
        '      <label class="pf-v5-c-switch">' +
        '        <input class="pf-v5-c-switch__input" type="checkbox" id="loadtest-enable-live" checked>' +
        '        <span class="pf-v5-c-switch__toggle"></span>' +
        '        <span class="pf-v5-c-switch__label" style="margin-left:8px;">Enable live watching</span>' +
        '      </label>' +
        '    </div>' +
        '  </div>' +
        '  <footer class="pf-v5-c-modal-box__footer">' +
        '    <button class="pf-v5-c-button pf-m-primary" id="loadtest-start-confirm">Start</button>' +
        '    <button class="pf-v5-c-button pf-m-link" id="loadtest-start-cancel">Cancel</button>' +
        '  </footer>' +
        '</div>';
    document.body.appendChild(backdrop);

    document.getElementById('loadtest-start-confirm').addEventListener('click', function () {
        var enableLive = document.getElementById('loadtest-enable-live').checked;
        backdrop.remove();
        startLoadTest(false, enableLive);
    });
    document.getElementById('loadtest-start-cancel').addEventListener('click', function () {
        backdrop.remove();
    });
}

function startLoadTest(force, enableLive) {
    var sel = document.getElementById('live-model-select');
    if (!sel.value) {
        document.getElementById('live-error-msg').textContent = 'Please select a model first.';
        show('live-error');
        return;
    }
    var parts = sel.value.split('/');
    var namespace = parts[0], model = parts[1];
    var tokenSize = parseInt(document.getElementById('loadtest-tokens').value);
    var concurrency = parseInt(document.getElementById('loadtest-concurrency').value);

    hide('live-error');
    var btn = document.getElementById('loadtest-start-btn');
    btn.disabled = true;

    // Show countdown when forcing (server waits for old runner to terminate)
    var countdownInterval = null;
    if (force) {
        var countdown = 30;
        btn.textContent = 'Removing old runner... ' + countdown + 's';
        countdownInterval = setInterval(function () {
            countdown--;
            if (countdown > 0) {
                btn.textContent = 'Removing old runner... ' + countdown + 's';
            } else {
                btn.textContent = 'Starting...';
                clearInterval(countdownInterval);
                countdownInterval = null;
            }
        }, 1000);
    } else {
        btn.textContent = 'Starting...';
    }

    function clearCountdown() {
        if (countdownInterval) { clearInterval(countdownInterval); countdownInterval = null; }
    }

    fetch('/api/loadtest/start', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ model: model, namespace: namespace, tokenSize: tokenSize, concurrency: concurrency, force: !!force })
    })
    .then(function (r) {
        clearCountdown();
        if (r.status === 409) {
            return r.json().then(function (data) {
                btn.disabled = false;
                btn.textContent = 'Start Load Test';
                showLoadTestConflict(data.activeUser, data.activeModel);
                throw { handled: true };
            });
        }
        if (!r.ok) return r.json().then(function (e) { throw new Error(e.error || 'Request failed'); });
        return r.json();
    })
    .then(function () {
        btn.disabled = true;
        btn.textContent = 'Running...';
        document.getElementById('loadtest-stop-btn').disabled = false;
        document.getElementById('loadtest-status-indicator').className = 'live-indicator-on';
        document.getElementById('loadtest-status-text').textContent = 'Running';
        document.getElementById('loadtest-tokens').disabled = true;
        document.getElementById('loadtest-concurrency').disabled = true;
        show('loadtest-results');
        startLoadTestPolling();
        if (enableLive) startLive();
    })
    .catch(function (err) {
        clearCountdown();
        if (err && err.handled) return;
        btn.disabled = false;
        btn.textContent = 'Start Load Test';
        document.getElementById('live-error-msg').textContent = 'Failed to start load test: ' + err.message;
        show('live-error');
    });
}

function showLoadTestConflict(activeUser, activeModel) {
    // Remove existing dialog if any
    var existing = document.getElementById('loadtest-conflict-dialog');
    if (existing) existing.remove();

    var backdrop = document.createElement('div');
    backdrop.id = 'loadtest-conflict-dialog';
    backdrop.className = 'pf-v5-c-backdrop';
    backdrop.innerHTML =
        '<div class="pf-v5-c-modal-box pf-m-warning pf-m-sm" role="dialog" aria-modal="true" style="position:fixed;top:50%;left:50%;transform:translate(-50%,-50%);z-index:10001;max-width:500px;">' +
        '  <div class="pf-v5-c-modal-box__header">' +
        '    <h1 class="pf-v5-c-modal-box__title">Load Test Already Running</h1>' +
        '  </div>' +
        '  <div class="pf-v5-c-modal-box__body">' +
        '    <p>Starting this will overwrite an ongoing load test on model <strong>' + esc(activeModel) + '</strong> running by user <strong>' + esc(activeUser) + '</strong>.</p>' +
        '    <p style="margin-top:8px;">Do you want to stop their test and start yours?</p>' +
        '  </div>' +
        '  <footer class="pf-v5-c-modal-box__footer">' +
        '    <button class="pf-v5-c-button pf-m-warning" id="loadtest-conflict-confirm">Overwrite and Start</button>' +
        '    <button class="pf-v5-c-button pf-m-link" id="loadtest-conflict-cancel">Cancel</button>' +
        '  </footer>' +
        '</div>';
    document.body.appendChild(backdrop);

    document.getElementById('loadtest-conflict-confirm').addEventListener('click', function () {
        backdrop.remove();
        startLoadTest(true, true);
    });
    document.getElementById('loadtest-conflict-cancel').addEventListener('click', function () {
        backdrop.remove();
    });
}

function stopLoadTest() {
    fetch('/api/loadtest/stop', { method: 'POST' })
    .then(handleJsonResponse)
    .then(function () {
        resetLoadTestUI();
    })
    .catch(function () {
        resetLoadTestUI();
    });
}

function resetLoadTestUI() {
    document.getElementById('loadtest-start-btn').disabled = false;
    document.getElementById('loadtest-start-btn').textContent = 'Start Load Test';
    document.getElementById('loadtest-stop-btn').disabled = true;
    document.getElementById('loadtest-status-indicator').className = 'live-indicator-off';
    document.getElementById('loadtest-status-text').textContent = 'Idle';
    document.getElementById('loadtest-tokens').disabled = false;
    document.getElementById('loadtest-concurrency').disabled = false;
    stopLoadTestPolling();
    // Update the banner to reflect stopped state
    document.getElementById('loadtest-active-status').textContent = 'Idle';
    document.getElementById('loadtest-active-status').style.color = '#3e8635';
}

function startLoadTestPolling() {
    stopLoadTestPolling();
    pollLoadTestStatus();
    loadTestStatusInterval = setInterval(pollLoadTestStatus, 2000);
}

function stopLoadTestPolling() {
    if (loadTestStatusInterval) {
        clearInterval(loadTestStatusInterval);
        loadTestStatusInterval = null;
    }
}

function pollLoadTestStatus() {
    fetch('/api/loadtest/status')
    .then(handleJsonResponse)
    .then(function (data) {
        renderLoadTestStats(data);
        if (!data.running && data.totalRequests > 0) {
            resetLoadTestUI();
            // Keep results visible
            show('loadtest-results');
        }
    })
    .catch(function () {
        // silently ignore poll errors
    });
}

function updateLoadTestBanner(data) {
    document.getElementById('loadtest-total-count').textContent = data.totalTestsStarted || 0;
    if (data.testRunning) {
        document.getElementById('loadtest-active-status').textContent = 'Running';
        document.getElementById('loadtest-active-status').style.color = '#0066cc';
    } else {
        document.getElementById('loadtest-active-status').textContent = 'Idle';
        document.getElementById('loadtest-active-status').style.color = '#3e8635';
    }
}

function renderLoadTestStats(data) {
    updateLoadTestBanner(data);
    var elapsed = data.elapsedSec || 0;
    var elapsedStr = elapsed < 60 ? elapsed.toFixed(1) + 's' : (elapsed / 60).toFixed(1) + 'm';
    var rps = data.requestsPerSec ? data.requestsPerSec.toFixed(2) : '0';
    var tps = data.tokensPerSec ? data.tokensPerSec.toFixed(1) : '0';
    var avgLat = data.avgLatencyMs ? data.avgLatencyMs.toFixed(0) + ' ms' : '-';
    var p50Lat = data.p50LatencyMs ? data.p50LatencyMs.toFixed(0) + ' ms' : '-';
    var p99Lat = data.p99LatencyMs ? data.p99LatencyMs.toFixed(0) + ' ms' : '-';
    var minLat = data.minLatencyMs ? data.minLatencyMs.toFixed(0) + ' ms' : '-';
    var maxLat = data.maxLatencyMs ? data.maxLatencyMs.toFixed(0) + ' ms' : '-';

    document.getElementById('loadtest-summary').innerHTML =
        liveSummaryCard('Total Requests', data.totalRequests || 0, '#0066cc') +
        liveSummaryCard('Successful', data.successful || 0, '#3e8635') +
        liveSummaryCard('Failed', data.failed || 0, (data.failed || 0) > 0 ? '#c9190b' : '#3e8635') +
        liveSummaryCard('Req/s', rps, '#0066cc') +
        liveSummaryCard('Tok/s', tps, '#31a354') +
        liveSummaryCard('Avg Latency', avgLat, '#6a3d9a') +
        liveSummaryCard('P50 Latency', p50Lat, '#6a3d9a') +
        liveSummaryCard('P99 Latency', p99Lat, '#e6550d') +
        liveSummaryCard('Min Latency', minLat, '#3e8635') +
        liveSummaryCard('Max Latency', maxLat, '#c9190b') +
        liveSummaryCard('Concurrency', data.concurrency || 0, '#6a6e73') +
        liveSummaryCard('Elapsed', elapsedStr, '#6a6e73');

    if (data.lastError) {
        document.getElementById('loadtest-last-error').textContent = data.lastError;
        show('loadtest-error-display');
    } else {
        hide('loadtest-error-display');
    }
}

// ========== Utilities ==========

function handleJsonResponse(r) {
    if (!r.ok) return r.json().then(function (e) { throw new Error(e.error || 'Request failed'); });
    return r.json();
}

function summaryCard(title, value, icon) {
    return '<div class="pf-v5-c-card summary-card"><div class="pf-v5-c-card__body summary-card-body">' +
        '<div class="summary-card-icon"><i class="pf-icon ' + icon + '"></i></div>' +
        '<div class="summary-card-text"><div class="summary-card-value">' + value + '</div>' +
        '<div class="summary-card-label">' + esc(title) + '</div></div></div></div>';
}

function statusBadge(status) {
    if (!status) return '<span class="pf-v5-c-label pf-m-outline"><span class="pf-v5-c-label__content">Unknown</span></span>';
    var color = 'outline';
    var s = String(status);
    if (s === 'Ready' || s === 'Running') color = 'green';
    else if (s === 'Pending') color = 'blue';
    else if (s === 'NotReady' || s === 'Failed') color = 'red';
    else if (s === 'Succeeded') color = 'green';
    return '<span class="pf-v5-c-label pf-m-' + color + '"><span class="pf-v5-c-label__content">' + esc(s) + '</span></span>';
}

function fmtDate(iso) {
    if (!iso) return '-';
    var d = new Date(iso);
    if (isNaN(d.getTime())) return iso;
    return d.toLocaleDateString() + ' ' + d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
}

function fmtTime(epoch) {
    var d = new Date(epoch * 1000);
    return d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
}

function fmtNum(v, decimals) {
    if (!v && v !== 0) return '';
    var f = parseFloat(v);
    if (isNaN(f)) return String(v);
    return f.toFixed(decimals || 0);
}

function fmtLargeNum(n) {
    if (n >= 1e9) return (n / 1e9).toFixed(1) + 'B';
    if (n >= 1e6) return (n / 1e6).toFixed(1) + 'M';
    if (n >= 1e3) return (n / 1e3).toFixed(1) + 'K';
    return Math.round(n).toString();
}

function fmtBytes(v) {
    var n = parseFloat(v);
    if (isNaN(n)) return v;
    // Might be in MiB (DCGM) or bytes
    if (n > 1e12) return (n / (1024 * 1024 * 1024 * 1024)).toFixed(1) + ' TiB';
    if (n > 1e9) return (n / (1024 * 1024 * 1024)).toFixed(1) + ' GiB';
    if (n > 1e6) return (n / (1024 * 1024)).toFixed(0) + ' MiB';
    if (n > 1e3) return (n / 1024).toFixed(0) + ' KiB';
    // DCGM reports in MiB directly
    if (n < 1e5 && n > 0) return n.toFixed(0) + ' MiB';
    return n.toFixed(0) + ' B';
}

function fmtChartVal(v) {
    if (v >= 1e6) return (v / 1e6).toFixed(1) + 'M';
    if (v >= 1e3) return (v / 1e3).toFixed(1) + 'K';
    if (v >= 100) return Math.round(v).toString();
    if (v >= 1) return v.toFixed(1);
    if (v >= 0.01) return v.toFixed(2);
    return v.toFixed(3);
}

function uniqueNamespaces(models) {
    var ns = {};
    models.forEach(function (m) { ns[m.namespace] = true; });
    return Object.keys(ns).length;
}

function fmtCores(v) {
    if (!v && v !== 0) return '-';
    var n = parseFloat(v);
    if (isNaN(n) || n === 0) return '-';
    if (n === Math.floor(n)) return n + ' cores';
    return n.toFixed(1) + ' cores';
}

function fmtCoresRange(req, lim) {
    var r = fmtCores(req);
    var l = fmtCores(lim);
    if (r === '-' && l === '-') return '-';
    if (l === '-' || r === l) return r;
    return r + ' / ' + l;
}

function fmtMemRange(req, lim) {
    var r = fmtMem(req);
    var l = fmtMem(lim);
    if (r === '-' && l === '-') return '-';
    if (l === '-' || r === l) return r;
    return r + ' / ' + l;
}

function fmtMem(bytes) {
    if (!bytes && bytes !== 0) return '-';
    var n = parseFloat(bytes);
    if (isNaN(n) || n === 0) return '-';
    if (n >= 1024 * 1024 * 1024 * 1024) return (n / (1024 * 1024 * 1024 * 1024)).toFixed(1) + ' TB';
    if (n >= 1024 * 1024 * 1024) return (n / (1024 * 1024 * 1024)).toFixed(1) + ' GB';
    if (n >= 1024 * 1024) return (n / (1024 * 1024)).toFixed(0) + ' MB';
    if (n >= 1024) return (n / 1024).toFixed(0) + ' KB';
    return n + ' B';
}

function esc(s) {
    if (s === null || s === undefined) return '';
    var div = document.createElement('div');
    div.appendChild(document.createTextNode(String(s)));
    return div.innerHTML;
}

function show(id) { document.getElementById(id).classList.remove('hidden'); }
function hide(id) { document.getElementById(id).classList.add('hidden'); }

// ========== User Workload Monitoring check ==========

function checkUserWorkloadMonitoring() {
    fetch('/api/status')
        .then(handleJsonResponse)
        .then(function (data) {
            if (!data.userWorkloadMonitoring) {
                showUWMWarning();
            }
        })
        .catch(function () {});
}

function showUWMWarning() {
    var existing = document.getElementById('uwm-warning-dialog');
    if (existing) return;

    var backdrop = document.createElement('div');
    backdrop.id = 'uwm-warning-dialog';
    backdrop.className = 'pf-v5-c-backdrop';
    backdrop.innerHTML =
        '<div class="pf-v5-c-modal-box pf-m-warning pf-m-md" role="dialog" aria-modal="true" style="position:fixed;top:50%;left:50%;transform:translate(-50%,-50%);z-index:10001;max-width:600px;">' +
        '  <div class="pf-v5-c-modal-box__header">' +
        '    <h1 class="pf-v5-c-modal-box__title"><span style="color:#f0ab00;margin-right:8px;">&#9888;</span> User Workload Monitoring Not Enabled</h1>' +
        '  </div>' +
        '  <div class="pf-v5-c-modal-box__body">' +
        '    <p>User Workload Monitoring is not enabled on this cluster. Without it, <strong>metrics, charts, and analysis features will have limited functionality</strong>.</p>' +
        '    <p style="margin-top:12px;font-weight:600;">To enable it:</p>' +
        '    <ol style="margin:8px 0 0 20px;line-height:1.8;">' +
        '      <li>Edit the <code>cluster-monitoring-config</code> ConfigMap in the <code>openshift-monitoring</code> namespace:</li>' +
        '      <li><pre style="background:#f0f0f0;padding:12px;border-radius:4px;margin:8px 0;font-size:0.85rem;overflow-x:auto;">oc edit configmap cluster-monitoring-config -n openshift-monitoring</pre></li>' +
        '      <li>Add or update the <code>config.yaml</code> data key:<pre style="background:#f0f0f0;padding:12px;border-radius:4px;margin:8px 0;font-size:0.85rem;overflow-x:auto;">data:\n  config.yaml: |\n    enableUserWorkload: true</pre></li>' +
        '      <li>Save and wait for the monitoring stack to reconcile (1-2 minutes).</li>' +
        '    </ol>' +
        '  </div>' +
        '  <footer class="pf-v5-c-modal-box__footer">' +
        '    <button class="pf-v5-c-button pf-m-primary" id="uwm-warning-close">I Understand</button>' +
        '  </footer>' +
        '</div>';
    document.body.appendChild(backdrop);

    document.getElementById('uwm-warning-close').addEventListener('click', function () {
        backdrop.remove();
    });
}

// ========== Status page ==========

function loadStatus() {
    show('status-loading');
    hide('status-content');

    fetch('/api/status')
        .then(handleJsonResponse)
        .then(function (data) {
            hide('status-loading');
            renderStatus(data.services || []);
            show('status-content');
        })
        .catch(function (err) {
            hide('status-loading');
            show('status-content');
            document.getElementById('status-table-body').innerHTML =
                '<tr><td colspan="3">Failed to load status: ' + esc(err.message) + '</td></tr>';
        });
}

function renderStatus(services) {
    var tbody = document.getElementById('status-table-body');
    tbody.innerHTML = '';
    services.forEach(function (svc) {
        var tr = document.createElement('tr');
        var icon, color;
        if (svc.status === 'connected') {
            icon = '&#10004;';
            color = '#3e8635';
        } else {
            icon = '&#10008;';
            color = '#c9190b';
        }
        tr.innerHTML =
            '<td>' + esc(svc.name) + '</td>' +
            '<td><span style="color:' + color + ';font-size:1.2rem;font-weight:700;">' + icon + '</span> ' +
            '<span style="color:' + color + ';">' + esc(svc.status === 'connected' ? 'Connected' : 'Unavailable') + '</span></td>' +
            '<td>' + esc(svc.details || '-') + '</td>';
        tbody.appendChild(tr);
    });
}
