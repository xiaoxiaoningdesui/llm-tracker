# 项目介绍

大语言模型开发过程中发现不同应用与模型交互的输入输出异常重要，但是很多框架客户端并没有完全记录交互过程，所以基于 [traefik](https://github.com/traefik/traefik) 开发了一款大语言模型 IO 跟踪器。

# 当前支持模型

1. ollama 部署的大模型
2. deepseek
3. openaisdk可以对接的大模型(理论上deepseek输入输出符合openai规范，所以应该可以接入，但是没经过测试)

# 特性与优化

1. 屏蔽了 input 中 tools 列表，由于每次交互都会带上 tools 列表，通常可能会几百个工具，所以将 tools 列表进行了屏蔽
2. 支持流式与非流式响应，流式响应不会阻塞进程，流式记录会在输出完毕后，拼接所有 chunk 的信息，结果展示与非流式无异
3. 支持 mcp/function_call/chat 模式
4. 输入与输出内容对一些特殊字符进行了转义，易于人类阅读

# 项目编译

```bash
go build -o llm-tracker.exe .\cmd\traefik\
```
# 启动方式
```bash
./llm-tracker.exe --configFile=config.toml
```
这个路径要有3个文件，二进制exe，config.toml和rules.toml

# 如何接入

在对接大模型的工作流 / 客户端 / 代码中，将访问大模型的 ip 和端口改为 127.0.0.1:1234 即可。

# 项目依赖

本项目主要依赖的项目为：[traefik](https://github.com/traefik/traefik)