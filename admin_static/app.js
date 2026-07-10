/* MiniMax Web Reverse Proxy — Admin UI Logic */
(function () {
    'use strict';

    const $ = (s) => document.querySelector(s);
    const targetUrl = $('#targetUrl');
    const localPort = $('#localPort');
    const statusDot = $('#statusDot');
    const statusText = $('#statusText');
    const targetDisplay = $('#targetDisplay');
    const portDisplay = $('#portDisplay');
    const connectivityDisplay = $('#connectivityDisplay');
    const proxyUrlDisplay = $('#proxyUrlDisplay');
    const saveBtn = $('#saveBtn');
    const testBtn = $('#testBtn');
    const resultArea = $('#resultArea');

    // ── Load Config ───────────────────────────────────────────
    async function loadConfig() {
        try {
            const r = await fetch('/admin/api/config');
            const cfg = await r.json();
            targetUrl.value = cfg.target_url || 'https://agent.minimaxi.com';
            localPort.value = cfg.local_port || 8000;
            targetDisplay.textContent = cfg.target_url || '-';
            portDisplay.textContent = cfg.local_port || '8000';
            proxyUrlDisplay.textContent = `http://localhost:${cfg.local_port || 8000}/`;
            updateStatus('ok', '运行中');
            connectivityDisplay.textContent = '未知 (点击"测试连接")';
        } catch (e) {
            updateStatus('err', '无法连接后端');
        }
    }

    // ── Save Config ───────────────────────────────────────────
    async function saveConfig() {
        const data = {
            target_url: targetUrl.value.trim() || 'https://agent.minimaxi.com',
            local_port: parseInt(localPort.value) || 8000,
        };
        saveBtn.disabled = true;
        saveBtn.textContent = '保存中...';

        try {
            const r = await fetch('/admin/api/config', {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify(data),
            });
            const result = await r.json();
            if (result.status === 'ok') {
                showResult('配置已保存。修改端口需重启服务。', 'success');
                targetDisplay.textContent = data.target_url;
                portDisplay.textContent = data.local_port;
                proxyUrlDisplay.textContent = `http://localhost:${data.local_port}/`;
            } else {
                showResult('保存失败: ' + (result.error || '未知错误'), 'error');
            }
        } catch (e) {
            showResult('保存失败: ' + e.message, 'error');
        } finally {
            saveBtn.disabled = false;
            saveBtn.textContent = '保存配置';
        }
    }

    // ── Test Connection ───────────────────────────────────────
    async function testConnection() {
        testBtn.disabled = true;
        testBtn.textContent = '测试中...';
        showResult('正在连接目标站点...', 'loading');

        try {
            const r = await fetch('/admin/api/test', { method: 'POST' });
            const data = await r.json();
            if (data.success) {
                updateStatus('ok', '已连接');
                connectivityDisplay.textContent = `正常 (HTTP ${data.status})`;
                showResult(`✅ 目标站点可达 (HTTP ${data.status})`, 'success');
            } else {
                connectivityDisplay.textContent = '连接失败';
                showResult(`❌ 连接失败: ${data.error || '未知错误'}`, 'error');
            }
        } catch (e) {
            connectivityDisplay.textContent = '连接失败';
            showResult('测试失败: ' + e.message, 'error');
        } finally {
            testBtn.disabled = false;
            testBtn.textContent = '测试连接';
        }
    }

    // ── Helpers ───────────────────────────────────────────────
    function updateStatus(type, text) {
        statusDot.className = 'dot ' + type;
        statusText.textContent = text;
    }

    function showResult(msg, type) {
        resultArea.style.display = 'block';
        resultArea.className = 'result ' + type;
        resultArea.innerHTML = type === 'loading' ? '<span class="spinner"></span>' + msg : msg;
    }

    function escapeHtml(t) {
        const d = document.createElement('div');
        d.textContent = t;
        return d.innerHTML;
    }

    // ── Events ────────────────────────────────────────────────
    saveBtn.addEventListener('click', saveConfig);
    testBtn.addEventListener('click', testConnection);

    targetUrl.addEventListener('input', () => {
        targetDisplay.textContent = targetUrl.value || '-';
    });
    localPort.addEventListener('input', () => {
        portDisplay.textContent = localPort.value || '8000';
        proxyUrlDisplay.textContent = `http://localhost:${localPort.value || 8000}/`;
    });

    // ── Init ──────────────────────────────────────────────────
    loadConfig();
})();
