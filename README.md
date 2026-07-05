<div align="center">

# 🛡️ Proxymic — AI Gateway

### Cut your LLM API costs by **40-70%** with zero code changes.

[![Live Benchmark](https://img.shields.io/badge/%F0%9F%93%8A_Live_Benchmark-View_Data-brightgreen?style=for-the-badge)](https://ai-gateway-production-c86a.up.railway.app/benchmark)
[![Try Demo](https://img.shields.io/badge/%F0%9F%8E%AE_Live_Demo-Try_It-blue?style=for-the-badge)](https://ai-gateway-production-c86a.up.railway.app/demo)
[![Deploy to Railway](https://img.shields.io/badge/%F0%9F%9A%80_Deploy_to_Railway-1_click-8B5CF6?style=for-the-badge)](https://railway.app/direct?template=https://github.com/Arnab758/ai-real&envs=UPSTREAM_API_KEY&UPSTREAM_API_KEY=your_groq_or_openai_api_key)

</div>

---

## 📊 Live Benchmark — Real Data from Production

| Metric | Value |
|--------|-------|
| Requests Processed | **149** |
| Cache Hit Rate | **71.1%** |
| Tokens Saved | **9,637** |
| Cost Saved | **$0.29** (and growing) |

> **See it live:** [Open the interactive benchmark page →](https://ai-gateway-production-c86a.up.railway.app/benchmark)
>
> Includes a savings calculator — enter your monthly LLM spend and see projected annual savings.

---

## 🚀 Deploy in 60 Seconds

### Option 1: Railway (Recommended — includes Redis)

[![Deploy to Railway](https://railway.app/button.svg)](https://railway.app/direct?template=https://github.com/Arnab758/ai-real&envs=UPSTREAM_API_KEY&UPSTREAM_API_KEY=your_groq_or_openai_api_key)

**1 click → enter your API key → done.**

### Option 2: Docker

```bash
git clone https://github.com/Arnab758/ai-proxy-gateway.git
cd ai-proxy-gateway
export UPSTREAM_API_KEY=gsk_your_groq_key
docker compose up -d

curl http://localhost:8080/health
# → {"status":"ok"}
```

### Option 3: Render

[![Deploy to Render](https://render.com/images/deploy-to-render-button.svg)](https://render.com/deploy?repo=https://github.com/Arnab758/ai-real)

Add `UPSTREAM_API_KEY` env var and a Redis addon.

---

## 💻 How to Connect

**Change one line of code.** Replace your OpenAI base URL:

```python
from openai import OpenAI

client = OpenAI(
    base_url="https://your-gateway.up.railway.app/v1",  # ← Change this
    api_key="sk-your-key"
)

# Add one header
client.extra_headers = {
    "X-Gateway-Token": "my-app"
}
```

**That's it.** Every request is now cached. Send the same prompt twice → second response comes from cache → you save money.

<details>
<summary><b>cURL example</b></summary>

```bash
curl -X POST https://your-gateway.up.railway.app/v1/chat/completions \
  -H "Content-Type: application/json" \
  -H "X-Gateway-Token: my-app" \
  -H "Authorization: Bearer $API_KEY" \
  -d '{"model": "gpt-4", "messages": [{"role": "user", "content": "Hello"}]}'
```
Response headers include `X-Gateway-Cache: HIT` or `MISS`, `X-Gateway-Confidence`, and `X-Gateway-Latency`.
</details>

---

## 🔥 What Makes This Different

| Feature | Proxymic | Cloudflare AI Gateway | GPTCache |
|---------|----------|----------------------|----------|
| Semantic caching | ✅ 95% threshold | ❌ Exact match only | ✅ Complex setup |
| Multi-strategy matching | ✅ HNSW + L1 + Jaccard + Fuzzy + Template | ❌ | ⚠️ Vector only |
| Agent loop killer | ✅ Detects & blocks runaway agents | ❌ | ❌ |
| Single binary deploy | ✅ `docker run` | ❌ | ❌ |
| Open source | ✅ MIT | ❌ | ✅ |
| Typo tolerance | ✅ Levenshtein + n-grams | ❌ | ❌ |
| Adaptive threshold | ✅ Self-tuning | ❌ | ❌ |

---

## 🧠 How It Works

```
Your App → Proxymic Gateway → [Cache Check]
                                   ↓
                        ┌──────────────────────┐
                        │  CACHE HIT (71%)      │ ← Returns instantly (~3ms)
                        │  Cache MISS (29%)     │ ← Calls LLM, caches response
                        └──────────────────────┘
```

**4-tier matching strategy:**
1. **Exact hash** — O(1) lookup for identical prompts
2. **Template matching** — "Weather in {city}" = single cached response
3. **HNSW vector search** — Semantic similarity (O(log n))
4. **Jaccard + Fuzzy** — Word overlap + typo tolerance

**Bonus features:**
- **Request deduplication** — 100 concurrent identical requests → 1 API call
- **Adaptive threshold tuning** — Self-adjusts based on traffic patterns
- **L1 hot cache** — LRU cache for popular prompts (O(1))
- **Agentic loop killer** — Multi-stage detection for runaway AI agents

---

## 📡 API Endpoints

| Endpoint | Description |
|----------|-------------|
| `/v1/chat/completions` | Main proxy endpoint with caching |
| `/dashboard` | Live analytics dashboard |
| `/benchmark` | Live benchmark with savings calculator |
| `/demo` | Interactive live demo |
| `/health` | Health check |
| `/metrics` | Prometheus metrics |

---

## 📦 Configuration

Edit `gateway.yaml`:

```yaml
cache:
  vector:
    similarity_threshold: 0.95  # How similar prompts must match
  ttl_hours: 24                  # Cache expiry

rate_limiter:
  max_requests: 60               # Per tenant per minute
```

---

## 📊 Real-World Savings

| Without Proxymic | With Proxymic |
|-----------------|---------------|
| 1,000,000 API calls/month | ~290,000 calls (71% hit rate) |
| $500/month | **$145/month** |
| ~1,500ms avg latency | **~3ms for cache hits** |

---

## 🛡️ Production: Zero-Downtime Setup

```python
def chat(messages, use_gateway=True):
    if use_gateway:
        try:
            resp = requests.post(
                "https://your-gateway.up.railway.app/v1/chat/completions",
                timeout=3
            )
            if resp.status_code == 200:
                return resp.json()
        except:
            pass
    # Fallback: call LLM directly (always works)
    return requests.post("https://api.openai.com/v1/...", ...).json()
```

---

## ❤️ Support & Feedback

- **Bug?** [Open an issue](https://github.com/Arnab758/ai-proxy-gateway/issues)
- **Idea?** [Start a discussion](https://github.com/Arnab758/ai-proxy-gateway/discussions)
- **Contribute?** PRs welcome!

**If this saves you money, give it a ⭐ — it helps others find it.**

---

<div align="center">
<p><b>Built with ❤️ in Go + HNSW + Redis</b></p>
<p>
  <a href="https://ai-gateway-production-c86a.up.railway.app/benchmark">Live Benchmark</a> ·
  <a href="https://ai-gateway-production-c86a.up.railway.app/demo">Live Demo</a> ·
  <a href="https://github.com/Arnab758/ai-proxy-gateway">GitHub</a>
</p>
</div>
