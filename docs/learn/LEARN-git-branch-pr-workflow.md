---
# Git 协作基础:分支、PR、合并与清理

> 一句话:**`main` 是永久主干,`fix/*` `test/*` 是用完即删的临时分支;`git push`
> 走 SSH key、`gh`/PR 创建走 API token,两套认证互相独立;网页 merge 后必须本地
> `git pull` 才同步;`A..B` 两点 diff 会骗你看到假"删除",真正合并用三方合并不丢代码。**

本文从一次真实的全流程提炼:新建空仓 → 推三个分支 → 网页开 PR 合并 → 同步 →
清理分支。读完你能独立走完一轮"开分支干活 → 合回主干 → 清理"的循环,并看懂中途
几个最容易吓人/踩坑的地方。

---

## 1. 命令速查(先背这一套循环)

一轮功能开发的完整生命周期,以后每次照抄:

```bash
# ① 从最新主干开新分支
git checkout main && git pull
git checkout -b fix/某问题            # fix/ test/ feat/ 只是给人看的命名约定

# ② 在分支上反复改 + 提交(可以提交很多次)
git add -A
git commit -m "fix: 说明改了啥"

# ③ 推到远端(首次带 -u 设上游,之后只需 git push)
git push -u origin fix/某问题

# ④ 在 GitHub 网页开 PR → Merge(记得勾 "Delete branch")

# ⑤ 把合并结果拉回本地
git checkout main && git pull

# ⑥ 删掉用完的本地分支
git branch -d fix/某问题
```

其它高频命令:

```bash
git remote -v                          # 看远端地址
git branch                             # 看本地分支
git ls-remote --heads origin           # 看远端分支
git fetch origin --prune               # 拉远端状态 + 清掉已删的远端分支引用
git branch --merged main               # 哪些本地分支已并入 main(可安全删)
git push origin --delete 分支名         # 删远端分支
git log --oneline -5                   # 看最近历史
```

---

## 2. 心智模型:两种分支

Git 分支**没有"类型"**,前缀(`fix/` `test/` `feat/`)纯粹是写给人看的命名约定。
真正的区别只有一个:**寿命**。

| 角色 | 例子 | 寿命 | 放什么 |
|---|---|---|---|
| **主干** | `main` | **永久** | 只放"已完成、稳定"的代码,是真相源 |
| **临时分支** | `fix/...` `test/...` | **用完即删** | 一个功能/修复的在途工作,合回主干后删除 |

一句话心法:**`main` 是永久主干,`fix/*` 这种是"用完就扔"的临时分支。** 合并后留着
不删,仓库会越积越乱。

---

## 3. 两套认证:SSH key vs API token(关键!)

最反直觉、也最值得记住的一点:**Git 传输和 GitHub API 用的是两套独立认证。**

| 操作 | 走什么 | 用什么认证 |
|---|---|---|
| `git clone / push / pull / fetch` | Git 传输协议 | **SSH key**(`git@github.com:...`) |
| `gh pr create`、网页 API、CI | GitHub REST/GraphQL **API** | **Personal Access Token (PAT)** |

真实现场:`git push` 一路成功,但 `gh pr create` 报
`Resource not accessible by personal access token (createPullRequest)`。

原因不是账号没权限,而是:push 走 SSH key(配好了),而 `gh pr create` 调 API 用的是
一个**细粒度 token**(`github_pat_...` 前缀),那个 token 没勾选对该仓库的
**Pull requests: write** 权限。

> **教训一:push 能用 ≠ gh/PR 能用。** 两套认证分开排查。想用 `gh pr create`,要么
> 给细粒度 token 补上 `Pull requests: Read and write` 权限,要么直接
> `gh auth login` 走浏览器 OAuth 换一个全权限 token。

### SSH key 是什么(`ed25519`)

```bash
ssh-keygen -t ed25519 -C "你的邮箱"     # 生成密钥对,一路回车
cat ~/.ssh/id_ed25519.pub               # 复制公钥,粘到 GitHub → Settings → SSH keys
ssh -T git@github.com                   # 测试,看到 "Hi 用户名!" 即成功
```

`ed25519` 是密钥**算法**(Edwards 曲线 + Curve25519):比 RSA 更快、更安全、公钥更短,
是 GitHub 官方推荐的现代首选。私钥(`id_ed25519`)永不外传,只把 `.pub` 公钥交给
GitHub。连接时 GitHub 用公钥出题、只有持私钥者能答对——所以比密码安全。

### commit 归属 ≠ 权限

contributor 列表是 GitHub 拿 **commit 里的邮箱**去匹配账号。如果 commit 用的邮箱
(`git config user.email`)没绑到你 GitHub 账号,头像就不显示在 contributor 里——但
这**完全不影响**你能否 push / merge(那只看你是不是仓库 owner / 有写权限)。要让历史
commit 正确归属,把那个邮箱加进 GitHub → Settings → Emails 即可,会自动认领。

---

## 4. PR 与 "Merge 按钮在哪"

新手最常见的卡点:**在仓库首页找不到 Merge 按钮。**

因为 **Merge 按钮不在仓库首页 —— 它只在一个已创建的 Pull Request 页面底部才出现。**
推完分支只是把代码传上去了,还没有 PR。正确顺序:

1. 仓库页 → **Pull requests** 标签 → **New pull request**
   (刚推完分支时首页常有黄色 "Compare & pull request" 提示条,点它更快)
2. 选两端:**base: `main`** ← **compare: `fix/某问题`**(把 fix 合进 main)
3. **Create pull request**
4. 进 PR 页面,**拉到底**,才看到绿色 **Merge pull request** 按钮

> **教训二:看不到 Merge 按钮,99% 是因为还没建 PR,而不是权限问题。**

个人单干嫌麻烦,也可以**完全不走 PR**,本地直接合(走 SSH,不碰 token):

```bash
git checkout main
git merge fix/某问题
git push
```

---

## 5. 合并的假象:两点 diff vs 三方合并

这是最容易**虚惊一场**的地方。场景:`main` 已经合并了 A 分支,你想再合 B 分支
(B 是从 A 合并**之前**的旧 main 分出来的)。你一看:

```bash
git diff main..test            # 显示删了 400 行!symbolfilters.go 全没了?!
```

**别慌,这是假象。** `A..B` 是**两点 diff**:它把"main 有、test 没有"的东西一律
显示成"删除"。但 test 只是从旧点分出来、本来就不含那些文件,并不是它删的。

看分支真实改了什么,要对比**共同祖先**(或直接看那个 commit 本身):

```bash
git merge-base main test                       # 找共同祖先
git diff $(git merge-base main test)..test     # 相对祖先的真实改动
git show --stat <commit>                        # 直接看某 commit 改了啥
```

真正 merge 用的是**三方合并**(以共同祖先为基准):git 知道那些文件是 main 后来加的、
test 从没碰过 → **会保留**,不会丢。实测合并输出 `1 file changed, 248 insertions(+)`
证实:只净增测试文件,没删任何代码。

> **教训三:`A..B` 两点 diff 的"删除"常是假象;判断合并是否丢代码,看三方合并结果
> (merge 的输出),或对比共同祖先,而不是看两点 diff。**

---

## 6. 网页操作后:本地必须同步

在 GitHub 网页点了 Merge,合并发生在**远端**,你本地的 `main` 毫不知情、还停在旧点。
必须拉回来:

```bash
git checkout main
git fetch origin --prune        # 拉远端最新状态
git merge --ff-only origin/main # 纯快进同步(等价于 git pull 的安全形式)
```

`git pull` = `fetch` + `merge`。当本地没有领先的提交时,这就是一次干净的快进。

> **教训四:网页 merge ≠ 本地已更新。养成网页操作后 `git pull` 的习惯。**

---

## 7. 清理已合并的分支

分支合进 main 后使命就完成了,留着是垃圾。删分支只是删"标签",commit 已安全躺在
main 历史里,**不会丢代码**:

```bash
# 本地(-d 会校验"确实已合并"才让删,安全)
git branch -d fix/某问题

# 远端(网页 merge 时若没勾 Delete branch,远端分支还在)
git push origin --delete fix/某问题
```

理想终态:本地和远端都只剩一个干净的 `main`。

---

## 8. 这次踩过的坑(一页速查)

| 现象 | 真相 |
|---|---|
| 看不到 Merge 按钮 | Merge 按钮在 **PR 页面底部**,不在仓库首页 → 先建 PR(§4) |
| `git push` 成功但 `gh pr create` 失败 | 两套认证:push 走 SSH,gh 走 **API token**;token 缺 PR 权限(§3) |
| contributor 不显示我 | commit 邮箱没绑到账号;**不影响 merge 权限**(§3) |
| `git diff main..test` 显示删了一堆 | 两点 diff 假象;三方合并不会丢(§5) |
| 网页 merge 了,本地却没变 | 合并在远端,需本地 `git pull`(§6) |

---

## 附:本仓库信息

- 远端:`git@github.com:Li8g/quantlab.git`(SSH 认证),默认分支 `main`。
- `gh` CLI 已安装;若要用 `gh pr create`,需 `gh auth login` 换全权限 token(§3)。
- 本文不涉及代码,是从一次真实 git 操作流程提炼的纯协作流程说明。
