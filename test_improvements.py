"""
测试脚本 - 验证 MiniMax2API 改进

运行方式:
    python test_improvements.py
"""

import sys
import json


def test_imports():
    """测试所有模块能否正常导入"""
    print("=" * 60)
    print("测试 1: 模块导入")
    print("=" * 60)
    
    modules = [
        "main",
        "proxy", 
        "config",
        "models",
        "auth",
        "minimax_adapter",
    ]
    
    for module in modules:
        try:
            __import__(module)
            print(f"✓ {module}.py - OK")
        except Exception as e:
            print(f"✗ {module}.py - FAILED: {e}")
            return False
    
    print()
    return True


def test_config():
    """测试配置管理"""
    print("=" * 60)
    print("测试 2: 配置管理")
    print("=" * 60)
    
    from config import config_manager, Account
    
    # 测试获取账号
    accounts = config_manager.get_accounts()
    print(f"✓ 已加载账号数: {len(accounts)}")
    
    # 测试账号属性
    if accounts:
        acc = accounts[0]
        print(f"✓ 账号名称: {acc.name}")
        print(f"✓ 认证模式: {acc.auth_mode}")
        print(f"✓ 失败计数: {getattr(acc, 'failure_count', 0)}")
        print(f"✓ 冷却中: {acc.on_cooldown}")
    
    # 测试配置验证
    try:
        config_manager.update_config({
            "proxy_api_keys": [],  # 无效：空列表
            "accounts": config_manager.config.accounts,
        })
        print("✗ 配置验证失败 - 应该拒绝空 proxy_api_keys")
        return False
    except ValueError as e:
        print(f"✓ 配置验证工作正常: {e}")
    
    print()
    return True


def test_account_status():
    """测试账号状态"""
    print("=" * 60)
    print("测试 3: 账号状态")
    print("=" * 60)
    
    from proxy import get_accounts_status
    
    status = get_accounts_status()
    print(f"✓ 获取到 {len(status)} 个账号状态")
    
    if status:
        acc = status[0]
        required_fields = [
            "name", "is_active", "on_cooldown", 
            "request_count", "failure_count", "last_used",
            "cooldown_until", "auth_mode"
        ]
        for field in required_fields:
            if field in acc:
                print(f"✓ 字段 '{field}': {acc[field]}")
            else:
                print(f"✗ 缺少字段: {field}")
                return False
    
    print()
    return True


def test_model_mapping():
    """测试模型映射"""
    print("=" * 60)
    print("测试 4: 模型映射")
    print("=" * 60)
    
    from config import resolve_model
    
    test_cases = {
        "gpt-4o": "MiniMax-M2.7",
        "gpt-4o-mini": "MiniMax-M2.5-highspeed",
        "claude-sonnet-4": "MiniMax-M2.7",
        "gemini-2.0-flash": "MiniMax-M2.7-highspeed",
        "MiniMax-M2.7": "MiniMax-M2.7",  # 直传
        "unknown-model": "unknown-model",  # 未知模型直传
    }
    
    for input_model, expected in test_cases.items():
        result = resolve_model(input_model)
        if result == expected:
            print(f"✓ {input_model} -> {result}")
        else:
            print(f"✗ {input_model} -> {result} (期望: {expected})")
            return False
    
    print()
    return True


def test_token_parsing():
    """测试 Token 解析"""
    print("=" * 60)
    print("测试 5: Token 解析")
    print("=" * 60)
    
    from minimax_adapter import parse_token
    
    # 测试格式 1: realUserID+JWTtoken
    token1 = "user123+eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9"
    jwt, uid = parse_token(token1)
    if jwt == "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9" and uid == "user123":
        print("✓ 解析 realUserID+JWT 格式成功")
    else:
        print(f"✗ 解析失败: jwt={jwt}, uid={uid}")
        return False
    
    # 测试格式 2: JWT only (会尝试从 payload 提取 user.id)
    token2 = "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJ1c2VyIjp7ImlkIjoiMTIzIn19.test"
    jwt, uid = parse_token(token2)
    print(f"✓ 解析纯 JWT: uid={uid}")
    
    print()
    return True


def main():
    """运行所有测试"""
    print("\n" + "=" * 60)
    print("MiniMax2API 改进验证测试")
    print("=" * 60 + "\n")
    
    tests = [
        ("模块导入", test_imports),
        ("配置管理", test_config),
        ("账号状态", test_account_status),
        ("模型映射", test_model_mapping),
        ("Token 解析", test_token_parsing),
    ]
    
    results = []
    for name, test_func in tests:
        try:
            result = test_func()
            results.append((name, result))
        except Exception as e:
            print(f"✗ 测试 '{name}' 异常: {e}")
            import traceback
            traceback.print_exc()
            results.append((name, False))
    
    # 汇总
    print("=" * 60)
    print("测试结果汇总")
    print("=" * 60)
    
    passed = sum(1 for _, result in results if result)
    total = len(results)
    
    for name, result in results:
        status = "✓ 通过" if result else "✗ 失败"
        print(f"{status} - {name}")
    
    print(f"\n总计: {passed}/{total} 测试通过")
    
    if passed == total:
        print("\n🎉 所有测试通过！")
        return 0
    else:
        print(f"\n⚠️  {total - passed} 个测试失败")
        return 1


if __name__ == "__main__":
    sys.exit(main())
