#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""
TGPan 前端汉化脚本 v2 —— 上下文感知版

为什么 v1 不行：
  v1 用「带引号的完整字面量」做白名单替换。但实测发现有些词同时出现在
  两种位置：

    children:"Delete"        <- 界面按钮，该翻译
    46:"Delete"              <- 键盘键码表，翻了就破坏键盘事件

    children:"Home"          <- 可能
    36:"Home"                <- 键盘键码表，绝对不能动

    3:return "Root"          <- React 内部组件类型名，不能动

  换句话说：光看「词」不够，必须看「词出现的上下文」。

v2 策略：只替换明确是界面文案的位置，即这些 JSX 属性/键的值：

    children:"..."     界面上的文字（最主要的来源）
    label:"..."        表单标签
    placeholder:"..."  输入框提示
    title:"..."        标题
    description:"..."  说明文字
    name:"..."         导航项名称（要额外限定位，因为 name 也用得很广）
    "aria-label":"..." 无障碍标签（读屏也是中文更好）

其余位置（数字后的值、对象属性、函数名等）一律不动。

额外安全措施：
  1. 替换后校验一批框架内部键名仍需存在
  2. 校验键盘键码表完整（数字: 后面跟英文键名的那些）
  3. 文件大小变化超出预期范围就中止
"""

import re
import sys
import os
import shutil

TARGETS = sys.argv[1:] if len(sys.argv) > 1 else ["index-BzkD_NMi.js"]
DRY_RUN = "--dry" in sys.argv

# ---------------------------------------------------------------------------
# 翻译表
# ---------------------------------------------------------------------------
ZH = {
    # 侧边导航
    "My Drive": "我的网盘",
    "Recent": "最近",
    "Shared": "我的分享",
    "Storage": "存储空间",

    # 菜单 / 弹窗标题
    "Profile Menu": "个人菜单",
    "Theme Menu": "主题菜单",
    "Choose Color": "选择颜色",

    # 通用动作
    "Create": "创建",
    "Creating": "创建中…",
    "Cancel": "取消",
    "Close": "关闭",
    "Delete": "删除",
    "Settings": "设置",
    "Search": "搜索",
    "Logout": "退出登录",
    "Reset": "重置",
    "Yes": "是",
    "No": "否",
    "Copy All": "全部复制",
    "Remove All": "全部移除",
    "Loading": "加载中",
    "Error": "出错了",
    "Other": "其他",

    # 频道管理
    "Channels": "频道",
    "Create Channel": "创建频道",
    "Channel Name": "频道名称",
    "Delete Channel": "删除频道",
    "Channel Added": "频道已添加",
    "Channel Deleted": "频道已删除",
    "Channels Synced": "频道已同步",
    "Remove All Bots": "移除所有机器人",
    "All bots removed": "所有机器人已移除",
    "Failed to remove bots": "移除机器人失败",
    "Select Default Channel": "选择默认频道",
    "Default channel updated": "默认频道已更新",
    "Tokens Copied": "令牌已复制",

    # 搜索面板
    "Keywords": "关键词",
    "Filename or regex...": "文件名或正则表达式…",
    "Exact Match": "精确匹配",
    "Regular Expression": "正则表达式",
    "Categories": "分类",
    "Current Folder": "当前文件夹",
    "Everywhere": "全部位置",
    "Include Subfolders": "包含子文件夹",
    "Date Modified": "修改日期",
    "From": "从",
    "To": "至",
    "Created": "已创建",

    # 设置页
    "Concurrent Part Uploads": "并发分片上传数",
    "Upload Retries": "上传重试次数",
    "Upload Retry Delay": "上传重试间隔",
    "Resizer Host": "图片缩放服务地址",
    "Page Size": "每页条目数",
    "Split File Size": "分片大小",
    "Encrypt Files": "加密文件",
    "Random Chunking": "随机分片",
    "Rclone Media Proxy": "Rclone 媒体代理",
    "Number of retries for each part upload": "每个分片的上传重试次数",
    "Delay between retries in milliseconds": "重试间隔（毫秒）",
    "Image Resize Host to resize images": "用于缩放图片的服务地址",
    "Number of items per page": "每页显示的条目数量",
    "Split File Size for multipart uploads": "分片上传时的分片大小",
    "Encrypt Files before uploading": "上传前加密文件",
    "Randomize Names of File Chunks": "随机化文件分片名称",
    "Play Files directly from Rclone Webdav": "通过 Rclone WebDAV 直接播放文件",

    # 主题
    "Dark": "深色",
    "Light": "浅色",

    # 错误
    "Invalid URL format": "网址格式不正确",
    "Invalid format": "格式不正确",
    "Hide Error": "隐藏错误详情",
    "Show Error": "显示错误详情",
    "Missing a plugin?": "缺少插件？",

    # ---- 分块加载的页面（storage / _view / share / login）----
    # 存储空间页
    "Overview of your drive usage": "网盘使用情况概览",
    "Total Files": "文件总数",
    "Total Storage": "总容量",
    "Upload Activity": "上传记录",
    "Used Space": "已用空间",
    "Files": "文件",
    "Storage Usage": "存储用量",
    "By Category": "按分类",
    "Largest Files": "最大的文件",

    # 文件操作弹窗
    "Create Folder": "新建文件夹",
    "Delete Files": "删除文件",
    "Drop to upload": "拖拽到此处上传",
    "Failed": "失败",
    "Rename": "重命名",
    "Link expiration date": "链接有效期",
    "Public link password": "公开链接密码",
    "Set expiration date": "设置有效期",
    "Set link password": "设置链接密码",
    "New Name": "新名称",
    "New Folder Name": "新文件夹名称",
    "Enter a name": "请输入名称",
    "Folder Created": "文件夹已创建",
    "File Renamed": "文件已重命名",
    "Deleted successfully": "删除成功",

    # 公开分享页
    "This link is password protected": "该链接已设置访问密码",
    "Unlock": "解锁",

    # 登录页
    "OTP Code": "验证码",
    "Phone Number": "手机号",
    "Invalid OTP Code": "验证码不正确",
    "Search country...": "搜索国家…",
    "Sign In": "登录",
    "Send Code": "发送验证码",
    "Code Sent": "验证码已发送",
}

# 允许替换的上下文（属性名 / 键名）
SAFE_ATTRS = ["children", "label", "placeholder", "title", "description",
              "aria-label", "header", "content", "message"]

# 这些词只在 children 上下文替换（因为 name/title 等位置它们可能是内部标识）
CHILDREN_ONLY = {"Create", "Cancel", "Close", "Delete", "Settings", "Search",
                 "Logout", "Reset", "Yes", "No", "Loading", "Error", "Other",
                 "Dark", "Light", "From", "To", "Created", "Choose Color"}

# 替换后必须仍存在的框架内部标识
FORBIDDEN_TOUCH = ["childList", "Meta", "Fragment", "Portal", "StrictMode",
                   "Suspense", "DOMContentLoaded", "onClick", "onChange",
                   "Context", "Provider", "Consumer"]


def process(TARGET):
    """处理单个文件；返回 0 表示成功，非 0 表示失败。"""
    if not os.path.exists(TARGET):
        print(f"❌ 找不到文件: {TARGET}")
        return 1

    with open(TARGET, encoding="utf-8", errors="surrogateescape") as f:
        src = f.read()
    orig = src

    if not DRY_RUN:
        backup = TARGET + ".en.bak"
        if not os.path.exists(backup):
            shutil.copy2(TARGET, backup)
            print(f"📦 原始英文版已备份: {backup}")

    report = []
    total = 0

    for en, zh in ZH.items():
        attrs = ["children"] if en in CHILDREN_ONLY else SAFE_ATTRS
        n = 0
        for attr in attrs:
            for quote in ('"', "'"):
                # 匹配: attr:"英文" 或 attr:"英文"（允许中间有空格差异）
                pat = re.compile(
                    r'(' + re.escape(attr) + r'\s*:\s*)' + re.escape(quote) +
                    re.escape(en) + re.escape(quote)
                )
                cnt = len(pat.findall(src))
                if cnt:
                    src = pat.sub(lambda m: m.group(1) + quote + zh + quote, src)
                    n += cnt
        # 导航项用的是 name:"X"
        if en in ("My Drive", "Recent", "Shared", "Storage"):
            pat = re.compile(r'(name\s*:\s*)("' + re.escape(en) + r'"|\'' + re.escape(en) + r'\')')
            cnt = len(pat.findall(src))
            if cnt:
                src = pat.sub(lambda m: m.group(1) + '"' + zh + '"', src)
                n += cnt

        if n:
            total += n
            report.append((en, zh, n))

    print(f"\n✅ 共替换 {total} 处，涉及 {len(report)} 条文案：\n")
    for en, zh, n in sorted(report, key=lambda x: -x[2]):
        print(f"   {n:2d}×  {en:45s} -> {zh}")

    # ---------------- 安全校验 ----------------
    print("\n🔍 安全校验：")
    ok = True

    for key in FORBIDDEN_TOUCH:
        # 注意：这些标识在压缩代码里出现形式不统一 ——
        #   childList / Meta / Context 等常带引号
        #   Provider / Consumer 是对象属性名，不带引号
        # 所以统一按「裸词出现次数」统计，只要没归零就算安全。
        c = len(re.findall(r'\b' + re.escape(key) + r'\b', src))
        c0 = len(re.findall(r'\b' + re.escape(key) + r'\b', orig))
        if c0 == 0:
            continue
        if c == 0 or c < c0 // 2:
            print(f"   ❌ {key} 异常：{c0} -> {c}")
            ok = False
        else:
            print(f"   ✓ {key} 完整（{c0} -> {c}）")

    # 键盘键码表完整性：形如 46:"Delete" / 36:"Home" 的数字键名必须保留英文
    kb_pat = re.compile(r'\b\d{1,3}:"([A-Za-z]{2,12})"')
    kb_before = set(kb_pat.findall(orig))
    kb_after = set(kb_pat.findall(src))
    if kb_before != kb_after:
        lost = kb_before - kb_after
        print(f"   ❌ 键盘键码表被破坏，丢失: {lost}")
        ok = False
    elif kb_after:
        print(f"   ✓ 键盘键码表完整（{len(kb_after)} 项）")

    delta = len(src) - len(orig)
    print(f"   ✓ 文件大小 {len(orig)} -> {len(src)} ({delta:+d} 字节)")

    # 中文替换应该使文件变小（中文 UTF-8 通常 1 字 3 字节，但英文词更长）
    if abs(delta) > 50000:
        print(f"   ❌ 大小变化异常（{delta}），可能误伤严重")
        ok = False

    if not ok:
        print("\n❌ 校验未通过，放弃写入。")
        return 1

    if total == 0:
        print("\n（该文件无可替换文案，跳过）")
        return 0

    if DRY_RUN:
        print("\n（dry-run 模式，未写入文件）")
        return 0

    with open(TARGET, "w", encoding="utf-8", errors="surrogateescape") as f:
        f.write(src)
    print(f"\n🎉 汉化完成 -> {TARGET}")
    return 0


def main():
    failed = 0
    for t in TARGETS:
        print("=" * 70)
        print(f"处理: {t}")
        print("=" * 70)
        if process(t) != 0:
            failed += 1
    print("\n" + "=" * 70)
    print(f"全部完成：{len(TARGETS)} 个文件，失败 {failed} 个")
    return 1 if failed else 0


if __name__ == "__main__":
    sys.exit(main())
