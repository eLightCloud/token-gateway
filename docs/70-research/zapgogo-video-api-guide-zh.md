# Zapgogo 视频生成 API 使用说明

更新日期：2026-09-09  
适用站点：https://zapgogo.xyz  
适用 API 密钥分组：`sd-token-official`

## 1. 接入准备

1. 登录 [Zapgogo](https://zapgogo.xyz)，进入「API 密钥」。
2. 创建密钥，选择分组 **sd-token-official**，设置合适的额度及有效期。
3. 确保账户余额和密钥剩余额度均充足，并且密钥没有排除所需模型。
4. 请求头使用 `Authorization: Bearer sk-你的密钥`。请使用本站签发的密钥，不要使用其他服务商的密钥。

API 基础地址为 `https://zapgogo.xyz/v1`。分组由密钥决定，无需在请求 JSON 中传入分组名称。不要将 API 密钥放入浏览器前端、公开仓库或分享链接。

## 2. 模型与输出规格

| 名称 | 请求中的 model | 本站可请求的分辨率 |
| --- | --- | --- |
| Seedance 2.0 标准版 | dreamina-seedance-2-0-260128 | 720p、1080p、4k |
| Seedance 2.0 Fast | dreamina-seedance-2-0-fast-260128 | 720p、1080p |
| Seedance 2.0 Mini | dreamina-seedance-2-0-mini-260615 | 720p、1080p |
| Seedance 2.5 | dreamina-seedance-2-5-260628 | 720p、1080p |

- 本分组不接受 480P 输出请求；请求会返回 HTTP 400。
- 4K 仅限标准版；其他三款的 4K 请求会返回 HTTP 400。
- 不填写分辨率时，默认输出目标为 720P。建议显式指定 `metadata.resolution`。
- 本文示例使用 4 秒、16:9。其他时长、画面比例和参考素材限制由所选模型决定；不符合模型要求时会返回错误。
- 分辨率与时长相同，不代表每次计费用量完全相同。
- 模型名称必须完整填写，不要省略版本后缀。可使用 `GET /v1/models` 查询密钥可访问的模型。

## 3. 计费说明

### 3.1 八折已包含在以下价格中

本分组当前倍率为 **0.8**。下表是本站现行客户单价，单位为 **美元 / 100 万计费 tokens**，已包含八折，计算时不要再次乘以 0.8。

| 模型 | 输出分辨率 | 无参考视频输入 | 有参考视频输入 |
| --- | --- | ---: | ---: |
| 标准版 | 720p | $5.60 | $3.44 |
| 标准版 | 1080p | $6.16 | $3.76 |
| 标准版 | 4k | $3.20 | $1.92 |
| Fast | 720p | $4.48 | $2.64 |
| Fast | 1080p | $4.48 | $4.48 |
| Mini | 720p | $2.80 | $1.68 |
| Mini | 1080p | $2.80 | $2.80 |
| Seedance 2.5 | 720p | $8.56 | $5.12 |
| Seedance 2.5 | 1080p | $9.36 | $5.60 |

「无参考视频输入」包括文生视频和仅使用图片作为参考的请求。「有参考视频输入」指通过 `metadata.content` 提交 `video_url` 类型素材，不是提示词里提到了“视频”。

各分辨率独立定价，不能将 720P 的参考视频单价套用到 1080P。上表是本站价格，并不表示所有输出规格均采用相同的模型供应商原生产品价格。供应商活动折扣不自动叠加；如价格调整，以提交任务时本站公布并生效的价格为准。

### 3.2 计算公式

```text
最终费用（USD）= 平台最终计费 tokens ÷ 1,000,000 × 表中单价
```

视频计费 tokens 不是提示词的文字 token 数。时长、输出规格、帧率以及参考视频输入等会影响最终用量。请以本站「使用日志」中的结算用量和实际扣费为准；不要仅凭模型结果里的某个 `usage` 字段、提示词长度或请求时长自行推断账单。

计算示例（仅演示公式，不是固定单次报价）：

| 示例 | 最终计费 tokens | 计算 | 费用 |
| --- | ---: | --- | ---: |
| 标准版 720P，无参考视频 | 87,300 | 87,300 ÷ 1,000,000 × 5.60 | $0.488880 |
| 标准版 1080P，无参考视频 | 196,425 | 196,425 ÷ 1,000,000 × 6.16 | $1.209978 |
| 标准版 4K，无参考视频 | 785,700 | 785,700 ÷ 1,000,000 × 3.20 | $2.514240 |

4K 的每百万 token 单价较低，不代表整条视频更便宜，仍需结合计费用量计算。金额显示可能有舍入差异。

### 3.3 预扣、结算与失败

- 提交任务时会预扣额度；预扣金额不是最终报价。
- 任务成功后按最终计费用量结算，多退少补。请保留足够余额，不要只按预扣额估算总成本。
- 参数校验阶段被拒绝的 480P 或不支持的 4K 请求不产生生成费用。
- 已接受的任务若最终失败，系统会执行退款流程；退款日志可能晚于失败状态出现。如迟迟未返还，请提供任务 ID 联系客服核查。
- 网络超时、停止轮询、关闭程序或下载失败不代表生成任务取消，也不等于自动退款。
- 同一条需求重复提交会创建多个任务，可能分别计费；查询已有任务不会创建新的生成任务。
- 调整价格或分组倍率不会追溯重算此前已提交的任务，已有任务按提交时保存的计费参数结算。

## 4. API 调用流程

```text
POST /v1/videos
    → 保存返回的 id
    → GET /v1/videos/{id} 查询进度
    → status=completed
    → GET /v1/videos/{id}/content 下载 MP4
```

### 4.1 提交视频生成任务

```http
POST https://zapgogo.xyz/v1/videos
Authorization: Bearer sk-你的密钥
Content-Type: application/json
```

```json
{
  "model": "dreamina-seedance-2-0-260128",
  "prompt": "一艘木质帆船缓缓驶过蓝色湖面，阳光照亮涟漪，远处是绿色山峦，镜头平稳，无文字。",
  "seconds": "4",
  "metadata": {
    "resolution": "720p",
    "ratio": "16:9",
    "generate_audio": false,
    "watermark": false
  }
}
```

| 字段 | 类型 | 说明 |
| --- | --- | --- |
| model | string | 完整模型 ID，见模型表 |
| prompt | string | 非空视频描述；建议写清主体、动作、场景和镜头 |
| seconds | string | 时长秒数，例如 `"4"`；请使用字符串 |
| metadata.resolution | string | `720p`、`1080p`，标准版另支持 `4k` |
| metadata.ratio | string | 示例使用 `16:9`，其他比例需符合模型要求 |
| metadata.generate_audio | boolean | 是否请求音频；能力由模型决定，示例为 `false` |
| metadata.watermark | boolean | 水印选项，示例为 `false` |
| images | string[] | 可选参考图片 URL 数组，适用于图生视频 |

也可以使用顶层 `resolution`，或 `size: "3840x2160"` 表示 4K。为避免冲突，建议只使用 `metadata.resolution`；同时提供时优先顺序为 `metadata.resolution`、顶层 `resolution`、`size`。

成功响应示意：

```json
{
  "id": "task_xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx",
  "object": "video",
  "status": "queued",
  "progress": 0
}
```

其他字段可能随任务阶段变化。请立即保存 `id`，后续查询与下载均使用该 ID。

### 4.2 查询状态

```http
GET https://zapgogo.xyz/v1/videos/{id}
Authorization: Bearer sk-你的密钥
```

常见状态：`queued`（排队）、`in_progress`（处理中）、`completed`（完成）、`failed`（失败）。建议每 10 秒查询一次。进度百分比不是承诺的剩余处理时间。

### 4.3 下载视频

```http
GET https://zapgogo.xyz/v1/videos/{id}/content
Authorization: Bearer sk-你的密钥
```

下载同样需要有效密钥，并且须属于有权访问该任务的账户。任务未完成时不能下载。接口支持 HTTP Range；建议完成后立即保存到自己的存储。站内成片当前临时保留约 48 小时，不是永久存储服务。

## 5. Python 完整示例

Python 3.10 或更新版本，先安装依赖：

```bash
python -m pip install requests
```

设置环境变量：

```powershell
# Windows PowerShell
$env:ZAPGOGO_API_KEY = "sk-替换为你的本站密钥"
```

```bash
# macOS / Linux
export ZAPGOGO_API_KEY="sk-替换为你的本站密钥"
```

将下方代码保存为 `zapgogo_video.py`，或使用配套的 [Python 示例文件](zapgogo_video.py)。

```python
"""Zapgogo video API example (Python 3.10+)."""
import argparse
import os
import time
from pathlib import Path

import requests

BASE_URL = "https://zapgogo.xyz/v1"
MODELS = (
    "dreamina-seedance-2-0-260128",
    "dreamina-seedance-2-0-fast-260128",
    "dreamina-seedance-2-0-mini-260615",
    "dreamina-seedance-2-5-260628",
)


def read_json(response):
    response.raise_for_status()
    data = response.json()
    if not isinstance(data, dict):
        raise RuntimeError("Unexpected API response")
    return data


def wait_for_video(session, task_id, timeout=1800):
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        try:
            response = session.get(
                f"{BASE_URL}/videos/{task_id}", timeout=(15, 60)
            )
        except requests.RequestException as exc:
            print(f"Query interrupted ({type(exc).__name__}); retrying...")
            time.sleep(10)
            continue
        with response:
            if response.status_code == 429 or 500 <= response.status_code < 600:
                time.sleep(10)
                continue
            data = read_json(response)
        status = str(data.get("status", "")).lower()
        print(f"{task_id}: {status}, progress={data.get('progress', 0)}%")
        if status in ("completed", "succeeded", "success"):
            return data
        if status in ("failed", "failure", "cancelled", "canceled"):
            raise RuntimeError(f"Video task failed: {data.get('error') or data}")
        time.sleep(10)
    raise TimeoutError(
        f"Stopped waiting, but the task was NOT cancelled: {task_id}. "
        "Resume with --task-id; do not submit the same request again."
    )


def download_video(session, task_id, output):
    path = Path(output)
    # 'xb' prevents overwriting an existing customer file.
    with session.get(
        f"{BASE_URL}/videos/{task_id}/content",
        stream=True,
        timeout=(15, 120),
    ) as response:
        response.raise_for_status()
        with path.open("xb") as file:
            try:
                for chunk in response.iter_content(chunk_size=1024 * 1024):
                    if chunk:
                        file.write(chunk)
            except Exception:
                file.close()
                path.unlink(missing_ok=True)
                raise
    print(f"Saved: {path.resolve()}")


def main():
    parser = argparse.ArgumentParser(description="Generate or resume a Zapgogo video")
    parser.add_argument("--model", choices=MODELS, default=MODELS[0])
    parser.add_argument("--resolution", choices=("720p", "1080p", "4k"), default="720p")
    parser.add_argument("--seconds", type=int, default=4)
    parser.add_argument("--prompt", default="A wooden sailboat on a blue lake, cinematic light.")
    parser.add_argument("--image-url", help="Optional publicly accessible reference image URL")
    parser.add_argument("--audio", action="store_true")
    parser.add_argument("--task-id", help="Resume an existing task without creating a new one")
    parser.add_argument("--output", default="video.mp4")
    args = parser.parse_args()

    api_key = os.environ.get("ZAPGOGO_API_KEY", "").strip()
    if not api_key:
        parser.error("Set ZAPGOGO_API_KEY to your site API key first")
    if Path(args.output).exists():
        parser.error("Output already exists; use a different --output path")
    if not args.task_id and args.resolution == "4k" and args.model != MODELS[0]:
        parser.error("4K is available only for Dreamina Seedance 2.0 Standard")
    if not args.task_id and not 4 <= args.seconds <= 15:
        parser.error("This example accepts 4-15 seconds; model-specific limits still apply")

    with requests.Session() as session:
        session.headers.update({"Authorization": f"Bearer {api_key}"})
        task_id = args.task_id
        if not task_id:
            payload = {
                "model": args.model,
                "prompt": args.prompt,
                "seconds": str(args.seconds),
                "metadata": {
                    "resolution": args.resolution,
                    "ratio": "16:9",
                    "generate_audio": args.audio,
                    "watermark": False,
                },
            }
            if args.image_url:
                payload["images"] = [args.image_url]
            # POST is deliberately sent once. A timeout can mean it was accepted.
            try:
                with session.post(
                    f"{BASE_URL}/videos", json=payload, timeout=(15, 120)
                ) as response:
                    data = read_json(response)
            except requests.RequestException:
                print(
                    "Submission was not confirmed. Check the task log before retrying; "
                    "a timeout does not prove that no task was created."
                )
                raise
            task_id = data.get("id") or data.get("task_id")
            if not isinstance(task_id, str) or not task_id:
                raise RuntimeError(f"Missing task ID: {data}")
            print(f"Task created: {task_id}", flush=True)
        wait_for_video(session, task_id)
        download_video(session, task_id, args.output)


if __name__ == "__main__":
    main()
```

运行示例：

```bash
# 720P 文生视频
python zapgogo_video.py --resolution 720p --output lake-720p.mp4

# 标准版 4K
python zapgogo_video.py --resolution 4k --output lake-4k.mp4

# 指定 Fast 模型
python zapgogo_video.py --model dreamina-seedance-2-0-fast-260128 --resolution 720p --output fast.mp4

# 使用公开可访问的参考图片
python zapgogo_video.py --image-url "https://你的图片域名/reference.jpg" --output image-video.mp4

# 程序中断后续查已有任务，不重复提交
python zapgogo_video.py --task-id task_替换为已有任务ID --output resumed.mp4
```

示例只自动重试查询阶段的临时错误，不会自动重试生成提交。等待超时后可用 `--task-id` 继续查询；更换输出文件名可避免覆盖已有文件。

## 6. 参考视频输入

需要参考视频时，可在请求的 `metadata` 中增加：

```json
{
  "content": [
    {
      "type": "video_url",
      "video_url": {
        "url": "https://你的素材域名/reference.mp4"
      },
      "role": "reference_video"
    }
  ]
}
```

素材地址必须可由服务端直接访问，不能是本地路径、登录后才能打开的链接或很快过期的地址。素材的格式、数量、时长及大小须符合模型要求；参考视频可能触发不同的用量规则，正式批量调用前请先进行小额验证。请确保拥有使用素材的权利。

## 7. 常见问题

| 情况 | 处理方法 |
| --- | --- |
| HTTP 400：不支持分辨率 | 不要请求 480P；4K 仅选标准版 |
| HTTP 400：参数或素材不合法 | 检查模型 ID、时长、画面比例、素材 URL 和响应中的错误说明 |
| HTTP 401 / 403 | 检查本站密钥、有效期、权限和账户状态 |
| 提示无可用渠道 / 模型不可用 | 确认密钥分组为 sd-token-official，且模型限制允许目标模型 |
| 提示额度不足 | 同时检查账户余额和密钥额度 |
| HTTP 429 / 暂时性 5xx | 降低并发；查询请求可退避重试，提交请求先确认是否已创建任务 |
| 提交超时，没有收到任务 ID | 先查看站内任务日志，确认是否已受理；不要立即重复提交 |
| 长时间未完成 | 保留任务 ID 继续查询，必要时交由客服核查 |
| 下载失败或文件过期 | 确认任务已完成、密钥有效且有访问权限；完成后应及时自行保存 |
| 费用与预扣不同 | 查看最终结算日志；预扣不是固定报价 |

排障时提供任务 ID、模型、分辨率、提交时间和错误内容即可，**不要发送完整 API 密钥**。

