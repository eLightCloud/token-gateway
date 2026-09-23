# Seedance 视频生成 API Python 调用说明

本文面向 `https://ai.codeyy.cn` 演示如何用 Python 完成一次 Seedance 视频生成请求，包括：

1. 配置用户 API Key；
2. 提交视频任务；
3. 轮询视频生成进度；
4. 判断失败或超时；
5. 下载最终 MP4 文件。

## 一、调用前准备

### 1. 获取用户 API Key

请在 `ai.codeyy.cn` 控制台创建或领取用户令牌。用户请求使用以下请求头：

```text
Authorization: Bearer sk-你的用户令牌
```

不要把管理员凭证或其他服务的密钥写进客户端代码。

### 2. 安装 Python 依赖

本文示例支持 Python 3.9 及以上版本，仅依赖 `requests`：

```bash
python -m pip install requests
```

### 3. 设置环境变量

Windows PowerShell：

```powershell
$env:AI_CODEYY_API_KEY = "sk-替换成你的用户令牌"
```

Linux 或 macOS：

```bash
export AI_CODEYY_API_KEY="sk-替换成你的用户令牌"
```

## 二、可用模型

当前可用的 Seedance 模型如下：

| 模型 | 用途 |
| --- | --- |
| `doubao-seedance-2-5-260628` | 推荐，Seedance 2.5 |
| `doubao-seedance-2-0-260128` | Seedance 2.0 |
| `doubao-seedance-2-0-fast-260128` | Seedance 2.0 Fast |
| `doubao-seedance-2-0-mini-260615` | Seedance 2.0 Mini |

请求时可以通过 `metadata.resolution` 选择 `480p`、`720p` 或 `1080p`。实际可用的时长、分辨率、宽高比和音频能力以所选模型为准。

## 三、完整 Python 示例

将以下代码保存为 `seedance_example.py`：

```python
import json
import os
import time
from pathlib import Path
from typing import Any, Optional
from urllib.parse import quote

import requests


BASE_URL = os.getenv("AI_CODEYY_BASE_URL", "https://ai.codeyy.cn").rstrip("/")
API_KEY = os.getenv("AI_CODEYY_API_KEY", "").strip()

MODEL = "doubao-seedance-2-5-260628"
POLL_INTERVAL_SECONDS = 10
MAX_WAIT_SECONDS = 30 * 60


def require_api_key() -> None:
    if not API_KEY:
        raise RuntimeError("请先设置环境变量 AI_CODEYY_API_KEY")
    if not API_KEY.startswith("sk-"):
        raise RuntimeError("AI_CODEYY_API_KEY 应为以 sk- 开头的用户令牌")


def build_session() -> requests.Session:
    session = requests.Session()
    session.headers.update(
        {
            "Authorization": f"Bearer {API_KEY}",
            "Accept": "application/json",
        }
    )
    return session


def request_json(
    session: requests.Session,
    method: str,
    path: str,
    **kwargs: Any,
) -> dict[str, Any]:
    response = session.request(
        method,
        f"{BASE_URL}{path}",
        timeout=(10, 120),
        **kwargs,
    )

    try:
        body = response.json()
    except ValueError as exc:
        response.raise_for_status()
        raise RuntimeError("服务器返回了非 JSON 响应") from exc

    if not response.ok:
        detail = json.dumps(body, ensure_ascii=False)
        raise RuntimeError(f"HTTP {response.status_code}: {detail}")
    if not isinstance(body, dict):
        raise RuntimeError(f"响应格式异常：{body!r}")
    return body


def create_video(
    session: requests.Session,
    prompt: str,
    resolution: str = "720p",
    ratio: str = "16:9",
    duration: int = 5,
    generate_audio: bool = False,
    image_url: Optional[str] = None,
) -> str:
    payload: dict[str, Any] = {
        "model": MODEL,
        "prompt": prompt,
        "metadata": {
            "resolution": resolution,
            "ratio": ratio,
            "duration": duration,
            "generate_audio": generate_audio,
            "watermark": False,
        },
    }

    # 图生视频时传入一张可由视频生成服务访问的 HTTPS 图片；文生视频则保持 None。
    if image_url:
        payload["images"] = [image_url]

    result = request_json(session, "POST", "/v1/videos", json=payload)
    task_id = result.get("id") or result.get("task_id")
    if not isinstance(task_id, str) or not task_id:
        raise RuntimeError(f"提交成功但未返回任务 ID：{result!r}")

    print("任务已提交：")
    print(json.dumps(result, ensure_ascii=False, indent=2))
    return task_id


def wait_for_video(
    session: requests.Session,
    task_id: str,
) -> dict[str, Any]:
    task_path = f"/v1/videos/{quote(task_id, safe='')}"
    started_at = time.monotonic()

    while True:
        task = request_json(session, "GET", task_path)
        status = str(task.get("status", "unknown")).lower()
        progress = task.get("progress", 0)
        print(f"任务状态：{status:<12} 进度：{progress}%")

        if status == "completed":
            return task

        if status == "failed":
            error = task.get("error") or {}
            if isinstance(error, dict):
                message = error.get("message") or error.get("code") or "未知错误"
            else:
                message = str(error)
            raise RuntimeError(f"视频任务失败：{message}")

        elapsed = time.monotonic() - started_at
        if elapsed >= MAX_WAIT_SECONDS:
            raise TimeoutError(
                f"等待任务超时（{MAX_WAIT_SECONDS} 秒），任务 ID：{task_id}"
            )

        time.sleep(POLL_INTERVAL_SECONDS)


def download_video(
    session: requests.Session,
    task_id: str,
    output_file: Path,
) -> Path:
    # 始终通过 API 的内容接口下载，避免把用户令牌发送给未知的第三方 URL。
    content_url = f"{BASE_URL}/v1/videos/{quote(task_id, safe='')}/content"
    output_file = output_file.expanduser().resolve()
    output_file.parent.mkdir(parents=True, exist_ok=True)
    temp_file = output_file.with_suffix(output_file.suffix + ".part")

    try:
        with session.get(
            content_url,
            stream=True,
            timeout=(10, 10 * 60),
        ) as response:
            response.raise_for_status()
            with temp_file.open("wb") as file:
                for chunk in response.iter_content(chunk_size=1024 * 1024):
                    if chunk:
                        file.write(chunk)
        temp_file.replace(output_file)
    except Exception:
        temp_file.unlink(missing_ok=True)
        raise

    print(f"视频已下载：{output_file}")
    return output_file


def main() -> None:
    require_api_key()
    session = build_session()

    task_id = create_video(
        session=session,
        prompt=(
            "电影感镜头，一只橘猫坐在雨后的霓虹街道旁，"
            "镜头缓慢向前推进，水面倒影清晰，自然运动"
        ),
        resolution="720p",
        ratio="16:9",
        duration=5,
        generate_audio=False,
        image_url=None,
    )

    completed_task = wait_for_video(session, task_id)
    print("任务已完成：")
    print(json.dumps(completed_task, ensure_ascii=False, indent=2))

    download_video(session, task_id, Path("seedance-result-720p.mp4"))


if __name__ == "__main__":
    try:
        main()
    except KeyboardInterrupt:
        print("\n用户取消运行")
        raise SystemExit(130)
    except Exception as exc:
        print(f"运行失败：{exc}")
        raise SystemExit(1)
```

运行：

```bash
python seedance_example.py
```

## 四、完整请求流程

### 第 1 步：提交任务

程序请求：

```text
POST https://ai.codeyy.cn/v1/videos
```

请求体中的关键字段：

| 字段 | 示例 | 说明 |
| --- | --- | --- |
| `model` | `doubao-seedance-2-5-260628` | 模型名称 |
| `prompt` | `电影感镜头……` | 视频提示词，不能为空 |
| `metadata.resolution` | `720p` | 目标分辨率 |
| `metadata.duration` | `5` | 视频时长，单位为秒 |
| `metadata.ratio` | `16:9` | 宽高比，也可按模型能力改为其他比例 |
| `metadata.generate_audio` | `false` | 是否生成音频，需所选模型支持 |
| `images` | `https://.../image.jpg` | 可选；传入后为图生视频 |

成功响应示例：

```json
{
  "id": "task_xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx",
  "task_id": "task_xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx",
  "object": "video",
  "model": "doubao-seedance-2-5-260628",
  "status": "queued",
  "progress": 0,
  "created_at": 1788350000
}
```

请保存 `id`，后续查询状态和下载视频都使用这个任务 ID。

### 第 2 步：轮询任务

程序每 10 秒请求一次：

```text
GET https://ai.codeyy.cn/v1/videos/{task_id}
```

可能出现的状态：

| `status` | 含义 | 客户端处理 |
| --- | --- | --- |
| `queued` | 已排队 | 继续轮询 |
| `in_progress` | 正在处理 | 继续轮询 |
| `completed` | 最终成品已准备好 | 开始下载 |
| `failed` | 任务失败 | 停止轮询并显示 `error` |
| `unknown` | 暂时无法确定状态 | 继续轮询，但仍受总超时时间限制 |

完成响应示例：

```json
{
  "id": "task_xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx",
  "task_id": "task_xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx",
  "object": "video",
  "model": "doubao-seedance-2-5-260628",
  "status": "completed",
  "progress": 100,
  "created_at": 1788350000,
  "completed_at": 1788350120,
  "metadata": {
    "url": "https://ai.codeyy.cn/v1/videos/task_xxx/content"
  }
}
```

### 第 3 步：下载成品

程序使用同一个用户令牌请求：

```text
GET https://ai.codeyy.cn/v1/videos/{task_id}/content
```

该接口返回视频文件流。示例程序会先写入 `.part` 临时文件，下载成功后再改名为最终的 `.mp4` 文件，避免中断时留下一个看似完整但实际损坏的视频。

## 五、图生视频示例

只需在 `create_video()` 调用中传入图片 URL：

```python
task_id = create_video(
    session=session,
    prompt="镜头缓慢推进，人物自然眨眼，头发随风轻轻摆动",
    resolution="720p",
    ratio="16:9",
    duration=5,
    generate_audio=False,
    image_url="https://example.com/input-image.jpg",
)
```

图片 URL 必须能被视频生成服务访问。不要使用只能在本机打开的路径，例如 `C:\\images\\input.jpg`。

## 六、常见错误

### `401 Unauthorized`

- 检查请求头是否为 `Authorization: Bearer sk-...`；
- 检查令牌是否有效、是否已禁用；
- 不要使用管理员令牌代替用户令牌。

### `403`、模型无权限或模型当前不可用

- 检查令牌是否允许所请求的模型；
- 检查账户余额是否充足。

### 长时间停留在 `queued` 或 `in_progress`

视频生成是异步任务，耗时会随视频长度和服务负载变化。不要高频请求；建议轮询间隔为 10～15 秒，并设置 20～30 分钟的总超时。

### 下载接口返回任务未完成

必须等到任务状态为 `completed` 后再请求 `/content`。
